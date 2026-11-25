package optionc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	defaultMaxContextInputTokens = 180000
	defaultHardContextTokenCap   = 200000
	defaultBaseWindowTurns       = 12
	defaultSummaryChunkTokens    = 2400
	defaultMaxSummaryDepth       = 3
	defaultSummaryNeedTokenLimit = 160000
	minProactiveHandoverTokens   = 32000
	handoverSummaryMaxChars      = 12000
	maxCheckpointHistoryMessages = 8
	handoverCompactionMode       = "handover_clean_context"
	handoverSummaryInstruction   = "summarise and provide a comprehensive summary report, which includes all the key details required for an effective handover. Include all your core findings, queries, artifacts etc for someone else to take over the investigation"
)

const (
	ContextHistoryModeRawMessages             = "raw_messages"
	ContextHistoryModeActiveCheckpointDelta   = "active_checkpoint_delta"
	ContextSummarySourceProvider              = "provider_summarizer"
	ContextSummarySourceDeterministicFallback = "deterministic_local_fallback"
	ContextSummarySourceActiveCheckpoint      = "active_checkpoint"
	ContextSummarySourceMixed                 = "mixed"
)

type SubLLMSummarizer func(ctx context.Context, text string, instruction string) (string, error)

type ContextRetrievalHint struct {
	QueryHint            string   `json:"query_hint"`
	CandidateMessageIDs  []string `json:"candidate_message_ids,omitempty"`
	CandidateArtifactIDs []string `json:"candidate_artifact_ids,omitempty"`
	MinScore             float64  `json:"min_score,omitempty"`
	OrderBy              string   `json:"order_by,omitempty"`
}

type ContextPolicy struct {
	MaxInputTokens     int
	HardContextCap     int
	BaseWindowTurns    int
	SummaryChunkTokens int
	MaxSummaryDepth    int
	NeedRetrievalLimit int
	EvidenceBudget     int
	CharsPerToken      int

	EnableSummary          bool
	NeedRetrievalByDefault bool
}

type ContextSummaryBucket struct {
	FromSeq       int64    `json:"from_seq"`
	ToSeq         int64    `json:"to_seq"`
	Tokens        int      `json:"tokens"`
	Summary       string   `json:"summary"`
	Messages      int      `json:"messages"`
	MessageIDs    []string `json:"message_ids,omitempty"`
	SummarySource string   `json:"summary_source,omitempty"`
	LocalFallback bool     `json:"local_fallback,omitempty"`
}

type ContextCheckpointPayload struct {
	ReplacementHistory    []types.ChatMessage `json:"replacement_history"`
	SourceMessageSeq      int64               `json:"source_message_seq"`
	SourceMessageID       string              `json:"source_message_id"`
	Summary               string              `json:"summary"`
	SummarySource         string              `json:"summary_source,omitempty"`
	ReferencedMessageIDs  []string            `json:"referenced_message_ids,omitempty"`
	ReferencedArtifactIDs []string            `json:"referenced_artifact_ids,omitempty"`
	TokenEstimate         int                 `json:"token_estimate"`
	CharsPerToken         int                 `json:"chars_per_token,omitempty"`
}

type RunContextPlan struct {
	SelectedMessageIDs   []string                  `json:"selected_message_ids"`
	SummarizedBuckets    []ContextSummaryBucket    `json:"summarized_buckets"`
	RetrievalHints       []ContextRetrievalHint    `json:"retrieval_hints"`
	SummaryRounds        int                       `json:"summary_rounds"`
	ArtifactRefsLoaded   int                       `json:"artifact_refs_loaded"`
	EvidenceBudget       int                       `json:"evidence_budget"`
	EvidenceUsed         int                       `json:"evidence_used"`
	EvidenceExhausted    bool                      `json:"evidence_exhausted"`
	NeedRetrieval        bool                      `json:"need_retrieval"`
	NeedRetrievalTooling bool                      `json:"need_retrieval_tooling"`
	PlanReason           string                    `json:"plan_reason"`
	TokensBefore         int                       `json:"tokens_before"`
	TokensAfter          int                       `json:"tokens_after"`
	HardCapped           bool                      `json:"hard_capped"`
	OverflowCompacted    bool                      `json:"overflow_compacted"`
	CompactionMode       string                    `json:"compaction_mode,omitempty"`
	HandoverSummaryUsed  bool                      `json:"handover_summary_used,omitempty"`
	HandoverSummaryChars int                       `json:"handover_summary_chars,omitempty"`
	Checkpoint           *ContextCheckpointPayload `json:"checkpoint,omitempty"`
	ActiveCheckpoint     *ContextCheckpointPayload `json:"active_checkpoint,omitempty"`
	HistoryMode          string                    `json:"history_mode,omitempty"`
	SummarySource        string                    `json:"summary_source,omitempty"`
	LocalFallbackUsed    bool                      `json:"local_fallback_used,omitempty"`
	ActiveCheckpointUsed bool                      `json:"active_checkpoint_used,omitempty"`
	CheckpointSourceSeq  int64                     `json:"checkpoint_source_seq,omitempty"`
	CheckpointSourceID   string                    `json:"checkpoint_source_id,omitempty"`
	DeltaMessageIDs      []string                  `json:"delta_message_ids,omitempty"`
	DeltaMessageCount    int                       `json:"delta_message_count,omitempty"`
}

type ContextBuildResult struct {
	History          []types.ChatMessage
	Plan             RunContextPlan
	SummaryForUser   string
	Checkpoint       *ContextCheckpointPayload
	ActiveCheckpoint *ContextCheckpointPayload
	DeltaMessages    []store.Message
}

type ContextBuildInput struct {
	RawMessages     []store.Message
	CheckpointDelta *ContextCheckpointDeltaInput
	UserMessageID   string
	HiddenContext   string
	Policy          ContextPolicy
}

type ContextCheckpointDeltaInput struct {
	Checkpoint    *ContextCheckpointPayload
	DeltaMessages []store.Message
}

type plannerSummaryResult struct {
	Text          string
	Tokens        int
	Source        string
	LocalFallback bool
}

type ContextPlanner struct {
	summarize SubLLMSummarizer
}

func NewContextPlanner(summarizer SubLLMSummarizer) *ContextPlanner {
	return &ContextPlanner{summarize: summarizer}
}

func (p *ContextPlanner) BuildFromInput(ctx context.Context, input ContextBuildInput) (ContextBuildResult, error) {
	if p == nil {
		return ContextBuildResult{}, nil
	}
	if input.CheckpointDelta != nil {
		return p.BuildFromCheckpointDelta(ctx, input.CheckpointDelta.Checkpoint, input.CheckpointDelta.DeltaMessages, input.UserMessageID, input.HiddenContext, input.Policy)
	}
	return p.Build(ctx, input.RawMessages, input.UserMessageID, input.HiddenContext, input.Policy)
}

func (p *ContextPlanner) BuildLocalFromInput(ctx context.Context, input ContextBuildInput) (ContextBuildResult, error) {
	if p == nil {
		return ContextBuildResult{}, nil
	}
	local := &ContextPlanner{}
	return local.BuildFromInput(ctx, input)
}

func (p *ContextPlanner) BuildLocal(ctx context.Context, msgs []store.Message, userMsgID string, hiddenContext string, policy ContextPolicy) (ContextBuildResult, error) {
	if p == nil {
		return ContextBuildResult{}, nil
	}
	local := &ContextPlanner{}
	return local.Build(ctx, msgs, userMsgID, hiddenContext, policy)
}

func (p *ContextPlanner) BuildFromCheckpointDelta(ctx context.Context, checkpoint *ContextCheckpointPayload, deltaMessages []store.Message, userMsgID string, hiddenContext string, policy ContextPolicy) (ContextBuildResult, error) {
	if p == nil {
		return ContextBuildResult{}, nil
	}
	return buildContextFromCheckpointDelta(ctx, checkpoint, deltaMessages, userMsgID, hiddenContext, policy)
}

func buildContextFromCheckpointDelta(ctx context.Context, checkpoint *ContextCheckpointPayload, deltaMessages []store.Message, userMsgID string, hiddenContext string, policy ContextPolicy) (ContextBuildResult, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ContextBuildResult{}, ctxErr
	}
	if checkpoint == nil {
		return ContextBuildResult{}, fmt.Errorf("active context checkpoint is required")
	}
	active := cloneContextCheckpointPayload(checkpoint)
	if active.SourceMessageSeq <= 0 || len(active.ReplacementHistory) == 0 {
		return ContextBuildResult{}, fmt.Errorf("active context checkpoint is incomplete")
	}

	policy = sanitizePolicy(policy)
	cpt := resolveCharsPerToken(policy.CharsPerToken)
	delta := orderedDeltaMessagesAfterCheckpoint(deltaMessages, active.SourceMessageSeq)
	deltaHistory := toChatHistory(delta, userMsgID, hiddenContext, "")
	deltaTokens := estimateHistoryTokens(deltaHistory, cpt)
	replacementBudget := checkpointReplacementTokenBudget(policy) - deltaTokens
	if replacementBudget <= 0 {
		replacementBudget = 1
	}
	replacement := boundCheckpointReplacementHistory(active.ReplacementHistory, replacementBudget, cpt)
	if len(replacement) == 0 {
		replacement = cloneChatHistory(active.ReplacementHistory)
	}
	history := append(cloneChatHistory(replacement), deltaHistory...)
	tokensAfter := estimateHistoryTokens(history, cpt)
	checkpointTokens := active.TokenEstimate
	if checkpointTokens <= 0 {
		checkpointTokens = estimateHistoryTokens(active.ReplacementHistory, cpt)
	}

	deltaIDs := idsFromMessages(delta)
	source := strings.TrimSpace(active.SummarySource)
	if source == "" {
		source = ContextSummarySourceActiveCheckpoint
	}
	plan := RunContextPlan{
		SelectedMessageIDs:   append([]string(nil), deltaIDs...),
		ArtifactRefsLoaded:   countReferencedArtifacts(delta),
		EvidenceBudget:       policy.EvidenceBudget,
		NeedRetrieval:        true,
		NeedRetrievalTooling: true,
		PlanReason:           ContextHistoryModeActiveCheckpointDelta,
		TokensBefore:         checkpointTokens + estimateMessagesTokens(delta, cpt),
		TokensAfter:          tokensAfter,
		OverflowCompacted:    true,
		CompactionMode:       handoverCompactionMode,
		ActiveCheckpoint:     cloneContextCheckpointPayload(active),
		HistoryMode:          ContextHistoryModeActiveCheckpointDelta,
		SummarySource:        source,
		ActiveCheckpointUsed: true,
		CheckpointSourceSeq:  active.SourceMessageSeq,
		CheckpointSourceID:   active.SourceMessageID,
		DeltaMessageIDs:      deltaIDs,
		DeltaMessageCount:    len(delta),
	}
	plan.RetrievalHints = buildActiveCheckpointRetrievalHints(active, deltaIDs, plan.ArtifactRefsLoaded)
	if policy.HardContextCap > 0 && (plan.TokensBefore > policy.HardContextCap || plan.TokensAfter > policy.HardContextCap) {
		plan.HardCapped = true
	}
	if policy.MaxInputTokens > 0 && plan.TokensAfter > policy.MaxInputTokens {
		plan.NeedRetrieval = true
		plan.NeedRetrievalTooling = true
	}

	return ContextBuildResult{
		History:          history,
		Plan:             plan,
		SummaryForUser:   strings.TrimSpace(active.Summary),
		ActiveCheckpoint: cloneContextCheckpointPayload(active),
		DeltaMessages:    append([]store.Message(nil), delta...),
	}, nil
}

func (p *ContextPlanner) Build(ctx context.Context, msgs []store.Message, userMsgID string, hiddenContext string, policy ContextPolicy) (ContextBuildResult, error) {
	if p == nil {
		return ContextBuildResult{}, nil
	}
	policy = sanitizePolicy(policy)
	visible := orderStoreMessagesBySeq(msgs)
	plan := RunContextPlan{HistoryMode: ContextHistoryModeRawMessages}
	plan.EvidenceBudget = policy.EvidenceBudget
	plan.NeedRetrievalTooling = policy.NeedRetrievalByDefault

	tokensBefore := estimateMessagesTokens(visible, policy.CharsPerToken)
	plan.TokensBefore = tokensBefore
	plan.NeedRetrieval = policy.NeedRetrievalByDefault
	plan.PlanReason = "within_budget"

	if len(visible) == 0 {
		return ContextBuildResult{History: []types.ChatMessage{}, Plan: plan}, nil
	}

	// If safely within budget, keep everything. Once the transcript is near the
	// model edge, switch to a handover context even if it is technically under
	// the soft cap; large prefill requests can otherwise stall before the
	// provider returns a context-window error.
	if tokensBefore <= handoverTriggerTokenLimit(policy) {
		h := sanitizeTrim(hiddenContext)
		history := toChatHistory(visible, userMsgID, h, "")
		plan.TokensAfter = estimateHistoryTokens(history, policy.CharsPerToken)
		plan.PlanReason = "within_budget"
		if policy.HardContextCap > 0 && plan.TokensAfter > policy.HardContextCap {
			plan.HardCapped = true
			plan.NeedRetrieval = true
			plan.PlanReason = "hard_cap_exceeded"
		}
		if plan.NeedRetrieval {
			plan.NeedRetrievalTooling = true
			if plan.PlanReason == "" || plan.PlanReason == "within_budget" {
				plan.PlanReason = "need_retrieval"
			}
		}
		return ContextBuildResult{History: history, Plan: plan}, nil
	}

	activeUser, ok := findMessageByID(visible, userMsgID)
	if !ok || activeUser.Role != types.RoleUser {
		return p.buildLegacyCompactedHistory(ctx, visible, userMsgID, hiddenContext, policy, plan)
	}

	priorMessages := make([]store.Message, 0, len(visible)-1)
	targetID := strings.TrimSpace(activeUser.ID)
	for _, msg := range visible {
		if strings.TrimSpace(msg.ID) == targetID {
			continue
		}
		priorMessages = append(priorMessages, msg)
	}

	plan.SelectedMessageIDs = idsFromMessages(priorMessages)
	plan.ArtifactRefsLoaded = countReferencedArtifacts(priorMessages)

	buckets := make([]ContextSummaryBucket, 0)
	if len(priorMessages) > 0 && policy.EnableSummary {
		for _, chunk := range chunkMessages(priorMessages, policy.SummaryChunkTokens, policy.CharsPerToken) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ContextBuildResult{}, ctxErr
			}
			summary, err := p.compressChunk(ctx, chunk, policy.MaxSummaryDepth, policy.CharsPerToken)
			if err != nil {
				return ContextBuildResult{}, err
			}
			if strings.TrimSpace(summary.Text) == "" {
				continue
			}
			from := chunk[0].Seq
			to := chunk[len(chunk)-1].Seq
			buckets = append(buckets, ContextSummaryBucket{
				FromSeq:       from,
				ToSeq:         to,
				Tokens:        summary.Tokens,
				Messages:      len(chunk),
				MessageIDs:    idsFromMessages(chunk),
				Summary:       summary.Text,
				SummarySource: summary.Source,
				LocalFallback: summary.LocalFallback,
			})
			plan.SummaryRounds++
		}
	}
	plan.SummarizedBuckets = buckets

	summary := p.buildHandoverSummary(ctx, priorMessages, buckets, policy.MaxSummaryDepth)
	summaryText := strings.TrimSpace(summary.Text)
	if summaryText == "" {
		summaryText = "Handover summary report: No prior thread context was available before this user turn."
		summary.Source = ContextSummarySourceDeterministicFallback
		summary.LocalFallback = true
	}
	plan.CompactionMode = handoverCompactionMode
	plan.HandoverSummaryUsed = true
	plan.HandoverSummaryChars = len([]rune(summaryText))
	plan.SummarySource = mergeSummarySource(summarySourceFromBuckets(buckets), summary.Source)
	plan.LocalFallbackUsed = summary.LocalFallback || bucketsUseLocalFallback(buckets)
	plan.OverflowCompacted = true
	plan.NeedRetrieval = true
	plan.NeedRetrievalTooling = true
	plan.PlanReason = handoverCompactionMode

	history := buildHandoverHistory(activeUser, hiddenContext, summaryText)
	plan.TokensAfter = estimateHistoryTokens(history, policy.CharsPerToken)
	plan.RetrievalHints = buildRetrievalHints(plan.SelectedMessageIDs, plan.ArtifactRefsLoaded, contextFromBuckets(buckets), len(buckets) > 0)
	if policy.HardContextCap > 0 && tokensBefore > policy.HardContextCap {
		plan.HardCapped = true
	}
	if policy.HardContextCap > 0 && plan.TokensAfter > policy.HardContextCap {
		plan.HardCapped = true
	}
	checkpoint := buildContextCheckpointPayloadWithMetadata(activeUser, summaryText, priorMessages, history, policy, plan.SummarySource)
	if checkpoint != nil {
		history = cloneChatHistory(checkpoint.ReplacementHistory)
		plan.TokensAfter = checkpoint.TokenEstimate
		plan.Checkpoint = cloneContextCheckpointPayload(checkpoint)
		if policy.HardContextCap > 0 && checkpoint.TokenEstimate > policy.HardContextCap {
			plan.HardCapped = true
		}
	}

	return ContextBuildResult{
		History:        history,
		Plan:           plan,
		SummaryForUser: summaryText,
		Checkpoint:     cloneContextCheckpointPayload(checkpoint),
	}, nil
}

func handoverTriggerTokenLimit(policy ContextPolicy) int {
	limit := policy.MaxInputTokens
	if limit <= 0 {
		limit = defaultMaxContextInputTokens
	}
	if policy.NeedRetrievalLimit > 0 && policy.NeedRetrievalLimit < limit {
		limit = policy.NeedRetrievalLimit
	}
	if policy.MaxInputTokens >= minProactiveHandoverTokens {
		proactive := (policy.MaxInputTokens * 8) / 10
		if proactive > 0 && proactive < limit {
			limit = proactive
		}
	}
	if limit <= 0 {
		return 1
	}
	return limit
}

func (p *ContextPlanner) buildLegacyCompactedHistory(
	ctx context.Context,
	visible []store.Message,
	userMsgID string,
	hiddenContext string,
	policy ContextPolicy,
	plan RunContextPlan,
) (ContextBuildResult, error) {
	window := policy.BaseWindowTurns
	if window <= 0 {
		window = defaultBaseWindowTurns
	}
	if window > len(visible) {
		window = len(visible)
	}

	kept := visible[len(visible)-window:]
	older := visible[:len(visible)-window]
	plan.SelectedMessageIDs = idsFromMessages(kept)
	plan.ArtifactRefsLoaded = countReferencedArtifacts(kept)

	buckets := make([]ContextSummaryBucket, 0)
	if len(older) > 0 && policy.EnableSummary {
		for _, chunk := range chunkMessages(older, policy.SummaryChunkTokens, policy.CharsPerToken) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ContextBuildResult{}, ctxErr
			}
			summary, err := p.compressChunk(ctx, chunk, policy.MaxSummaryDepth, policy.CharsPerToken)
			if err != nil {
				return ContextBuildResult{}, err
			}
			from := chunk[0].Seq
			to := chunk[len(chunk)-1].Seq
			buckets = append(buckets, ContextSummaryBucket{
				FromSeq:       from,
				ToSeq:         to,
				Tokens:        summary.Tokens,
				Messages:      len(chunk),
				MessageIDs:    idsFromMessages(chunk),
				Summary:       summary.Text,
				SummarySource: summary.Source,
				LocalFallback: summary.LocalFallback,
			})
			plan.SummaryRounds++
			plan.PlanReason = "summarized_older_turns"
		}
	}

	plan.SummarizedBuckets = buckets
	plan.SummarySource = summarySourceFromBuckets(buckets)
	plan.LocalFallbackUsed = bucketsUseLocalFallback(buckets)
	summaryText := summarizeBucketsText(buckets)
	summaryText = strings.TrimSpace(summaryText)

	if summaryText == "" {
		if policy.NeedRetrievalByDefault {
			plan.PlanReason = "no_summary"
		}
	}

	summaryForUser := combineHiddenParts(summaryText, hiddenContext)
	history := toChatHistory(kept, userMsgID, summaryForUser, "")
	tokensAfter := 0
	for _, m := range history {
		tokensAfter += estimateStringTokens(safeContent(m.Content), policy.CharsPerToken)
	}
	plan.TokensAfter = tokensAfter
	plan.RetrievalHints = buildRetrievalHints(plan.SelectedMessageIDs, plan.ArtifactRefsLoaded, contextFromBuckets(buckets), len(buckets) > 0)
	if tokensAfter > policy.MaxInputTokens {
		plan.NeedRetrieval = true
		if plan.PlanReason == "within_budget" {
			plan.PlanReason = "overflow_after_summary"
		}
	}
	if policy.NeedRetrievalLimit > 0 && tokensAfter > policy.NeedRetrievalLimit {
		plan.NeedRetrieval = true
	}
	if policy.HardContextCap > 0 && plan.TokensBefore > policy.HardContextCap {
		plan.NeedRetrieval = true
	}
	if policy.HardContextCap > 0 && tokensAfter > policy.HardContextCap {
		plan.HardCapped = true
		plan.NeedRetrieval = true
		plan.PlanReason = "hard_cap_exceeded"
	}

	if plan.NeedRetrieval {
		plan.NeedRetrievalTooling = true
		if plan.PlanReason == "" {
			plan.PlanReason = "need_retrieval"
		}
	}

	return ContextBuildResult{
		History:        history,
		Plan:           plan,
		SummaryForUser: summaryText,
	}, nil
}

func findMessageByID(msgs []store.Message, messageID string) (store.Message, bool) {
	target := strings.TrimSpace(messageID)
	if target == "" {
		return store.Message{}, false
	}
	for _, msg := range msgs {
		if strings.TrimSpace(msg.ID) == target {
			return msg, true
		}
	}
	return store.Message{}, false
}

func orderedDeltaMessagesAfterCheckpoint(msgs []store.Message, sourceSeq int64) []store.Message {
	ordered := orderStoreMessagesBySeq(msgs)
	out := make([]store.Message, 0, len(ordered))
	for _, msg := range ordered {
		if msg.Seq <= sourceSeq {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func orderStoreMessagesBySeq(msgs []store.Message) []store.Message {
	out := append([]store.Message(nil), msgs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		leftID := strings.TrimSpace(out[i].ID)
		rightID := strings.TrimSpace(out[j].ID)
		if leftID != rightID {
			return leftID < rightID
		}
		return false
	})
	return out
}

func checkpointReferencedMessageIDsForHints(checkpoint *ContextCheckpointPayload, deltaIDs []string) []string {
	if checkpoint == nil {
		return append([]string(nil), deltaIDs...)
	}
	out := make([]string, 0, len(checkpoint.ReferencedMessageIDs)+len(deltaIDs))
	seen := map[string]struct{}{}
	for _, id := range append(append([]string(nil), checkpoint.ReferencedMessageIDs...), deltaIDs...) {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func buildActiveCheckpointRetrievalHints(checkpoint *ContextCheckpointPayload, deltaIDs []string, deltaArtifactRefs int) []ContextRetrievalHint {
	ids := checkpointReferencedMessageIDsForHints(checkpoint, deltaIDs)
	out := make([]ContextRetrievalHint, 0, 2)
	if len(ids) > 0 {
		out = append(out, ContextRetrievalHint{
			QueryHint:           "Retrieve source messages referenced by active context checkpoint before expanding delta history",
			CandidateMessageIDs: ids,
			MinScore:            0.05,
			OrderBy:             "seq_asc",
		})
	}
	artifactRefs := deltaArtifactRefs
	if checkpoint != nil {
		artifactRefs += len(checkpoint.ReferencedArtifactIDs)
	}
	if artifactRefs > 0 {
		out = append(out, ContextRetrievalHint{
			QueryHint: "Search artifacts referenced by active context checkpoint or post-checkpoint delta",
			MinScore:  0.10,
			OrderBy:   "score_desc",
		})
	}
	return out
}

func buildHandoverHistory(activeUser store.Message, hiddenContext string, summaryText string) []types.ChatMessage {
	summary := strings.TrimSpace(summaryText)
	if summary == "" {
		summary = "Handover summary report: Context compaction produced an empty summary."
	}
	if len([]rune(summary)) > handoverSummaryMaxChars {
		summary = truncateRunes(summary, handoverSummaryMaxChars)
	}

	history := make([]types.ChatMessage, 0, 2)
	history = append(history, types.ChatMessage{
		Role:    types.RoleAssistant,
		Content: ptrString(summary),
	})

	content := combineHiddenParts(safeContent(activeUser.Content), hiddenContext)
	msg := types.ChatMessage{
		Role:    types.RoleUser,
		Content: ptrString(content),
	}
	history = append(history, msg)
	return history
}

func buildContextCheckpointPayload(
	source store.Message,
	summaryText string,
	summaryMessages []store.Message,
	replacementHistory []types.ChatMessage,
	policy ContextPolicy,
) *ContextCheckpointPayload {
	return buildContextCheckpointPayloadWithMetadata(source, summaryText, summaryMessages, replacementHistory, policy, "")
}

func buildContextCheckpointPayloadWithMetadata(
	source store.Message,
	summaryText string,
	summaryMessages []store.Message,
	replacementHistory []types.ChatMessage,
	policy ContextPolicy,
	summarySource string,
) *ContextCheckpointPayload {
	sourceID := strings.TrimSpace(source.ID)
	if sourceID == "" || source.Seq <= 0 || len(replacementHistory) == 0 {
		return nil
	}
	cpt := resolveCharsPerToken(policy.CharsPerToken)
	budget := checkpointReplacementTokenBudget(policy)
	history := boundCheckpointReplacementHistory(replacementHistory, budget, cpt)
	if len(history) == 0 {
		return nil
	}
	tokenEstimate := estimateHistoryTokens(history, cpt)
	summary := strings.TrimSpace(summaryText)
	if len([]rune(summary)) > handoverSummaryMaxChars {
		summary = truncateRunes(summary, handoverSummaryMaxChars)
	}
	summarySource = strings.TrimSpace(summarySource)
	if summarySource == "" {
		summarySource = ContextSummarySourceDeterministicFallback
	}

	return &ContextCheckpointPayload{
		ReplacementHistory:    history,
		SourceMessageSeq:      source.Seq,
		SourceMessageID:       sourceID,
		Summary:               summary,
		SummarySource:         summarySource,
		ReferencedMessageIDs:  checkpointReferencedMessageIDs(summaryMessages),
		ReferencedArtifactIDs: checkpointReferencedArtifactIDs(append(append([]store.Message(nil), summaryMessages...), source)),
		TokenEstimate:         tokenEstimate,
		CharsPerToken:         cpt,
	}
}

func checkpointReplacementTokenBudget(policy ContextPolicy) int {
	soft := policy.MaxInputTokens
	hard := policy.HardContextCap
	switch {
	case soft > 0 && hard > 0 && hard < soft:
		return hard
	case soft > 0:
		return soft
	case hard > 0:
		return hard
	default:
		return defaultMaxContextInputTokens
	}
}

func boundCheckpointReplacementHistory(history []types.ChatMessage, maxTokens int, charsPerToken int) []types.ChatMessage {
	if len(history) == 0 {
		return nil
	}
	cpt := resolveCharsPerToken(charsPerToken)
	if maxTokens <= 0 {
		maxTokens = defaultMaxContextInputTokens
	}
	out := cloneChatHistory(history)
	if len(out) > maxCheckpointHistoryMessages {
		trimmed := make([]types.ChatMessage, 0, maxCheckpointHistoryMessages)
		trimmed = append(trimmed, out[0])
		tail := out[len(out)-(maxCheckpointHistoryMessages-1):]
		trimmed = append(trimmed, tail...)
		out = trimmed
	}

	maxBytes := maxTokens * cpt
	if maxBytes <= 0 {
		maxBytes = cpt
	}
	excess := checkpointHistoryContentBytes(out) - maxBytes
	if excess <= 0 {
		return boundCheckpointReplacementTokens(out, maxTokens, cpt)
	}
	trimOrder := checkpointTrimOrder(len(out))
	trimCheckpointHistoryBytes(out, trimOrder, excess)
	return boundCheckpointReplacementTokens(out, maxTokens, cpt)
}

func trimCheckpointHistoryBytes(history []types.ChatMessage, trimOrder []int, excess int) {
	for _, idx := range trimOrder {
		if excess <= 0 {
			return
		}
		removed := trimCheckpointHistoryMessageBytes(history, idx, excess)
		excess -= removed
	}
}

func boundCheckpointReplacementTokens(history []types.ChatMessage, maxTokens int, charsPerToken int) []types.ChatMessage {
	cpt := resolveCharsPerToken(charsPerToken)
	if maxTokens <= 0 {
		maxTokens = defaultMaxContextInputTokens
	}
	trimOrder := checkpointTrimOrder(len(history))
	for {
		estimate := estimateHistoryTokens(history, cpt)
		if estimate <= maxTokens {
			return history
		}
		removeBytes := max(1, (estimate-maxTokens)*cpt)
		progress := false
		for _, idx := range trimOrder {
			if trimCheckpointHistoryMessageBytes(history, idx, removeBytes) > 0 {
				progress = true
				break
			}
		}
		if !progress {
			return history
		}
	}
}

func trimCheckpointHistoryMessageBytes(history []types.ChatMessage, idx int, removeBytes int) int {
	if idx < 0 || idx >= len(history) || history[idx].Content == nil || removeBytes <= 0 {
		return 0
	}
	content := strings.TrimSpace(*history[idx].Content)
	if content == "" {
		history[idx].Content = nil
		return 0
	}
	remove := minInt(removeBytes, len(content))
	keep := len(content) - remove
	next := truncateStringBytes(content, keep)
	history[idx].Content = ptrString(next)
	return len(content) - len(next)
}

func checkpointTrimOrder(n int) []int {
	if n <= 0 {
		return nil
	}
	out := make([]int, 0, n)
	for i := 0; i < n-1; i++ {
		out = append(out, i)
	}
	out = append(out, n-1)
	return out
}

func checkpointHistoryContentBytes(history []types.ChatMessage) int {
	total := 0
	for _, msg := range history {
		total += len(safeContent(msg.Content))
	}
	return total
}

func checkpointReferencedMessageIDs(msgs []store.Message) []string {
	ordered := orderedMessagesForCheckpoint(msgs)
	out := make([]string, 0, len(ordered))
	seen := map[string]struct{}{}
	for _, msg := range ordered {
		id := strings.TrimSpace(msg.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func checkpointReferencedArtifactIDs(msgs []store.Message) []string {
	ordered := orderedMessagesForCheckpoint(msgs)
	out := make([]string, 0)
	seen := map[string]struct{}{}
	for _, msg := range ordered {
		for _, id := range msg.ReferencedArtifactIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func orderedMessagesForCheckpoint(msgs []store.Message) []store.Message {
	out := append([]store.Message(nil), msgs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return strings.TrimSpace(out[i].ID) < strings.TrimSpace(out[j].ID)
	})
	return out
}

func cloneContextCheckpointPayload(in *ContextCheckpointPayload) *ContextCheckpointPayload {
	if in == nil {
		return nil
	}
	out := *in
	out.ReplacementHistory = cloneChatHistory(in.ReplacementHistory)
	out.ReferencedMessageIDs = append([]string(nil), in.ReferencedMessageIDs...)
	out.ReferencedArtifactIDs = append([]string(nil), in.ReferencedArtifactIDs...)
	return &out
}

func cloneChatHistory(in []types.ChatMessage) []types.ChatMessage {
	if len(in) == 0 {
		return nil
	}
	out := make([]types.ChatMessage, 0, len(in))
	for _, msg := range in {
		out = append(out, cloneChatMessage(msg))
	}
	return out
}

func cloneChatMessage(msg types.ChatMessage) types.ChatMessage {
	out := msg
	if msg.Seq != nil {
		seq := *msg.Seq
		out.Seq = &seq
	}
	if msg.Content != nil {
		content := *msg.Content
		out.Content = &content
	}
	out.ProviderPayload = append([]byte(nil), msg.ProviderPayload...)
	out.ToolCalls = append([]types.ToolCall(nil), msg.ToolCalls...)
	out.ImageAttachments = append([]types.ImageAttachment(nil), msg.ImageAttachments...)
	out.FileAttachments = append([]types.FileAttachment(nil), msg.FileAttachments...)
	return out
}

func truncateStringBytes(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if maxBytes <= 0 || s == "" {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := 0
	for idx, r := range s {
		next := idx + len(string(r))
		if next > maxBytes {
			break
		}
		end = next
	}
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(s[:end])
}

func (p *ContextPlanner) buildHandoverSummary(ctx context.Context, prior []store.Message, buckets []ContextSummaryBucket, maxDepth int) plannerSummaryResult {
	source := strings.TrimSpace(summarizeBucketsText(buckets))
	sourceMeta := summarySourceFromBuckets(buckets)
	localFallback := bucketsUseLocalFallback(buckets)
	if source == "" && len(prior) > 0 {
		source = strings.TrimSpace(messagesText(prior))
		sourceMeta = ContextSummarySourceDeterministicFallback
		localFallback = true
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return plannerSummaryResult{}
	}

	summary := source
	if p.summarize != nil {
		currentDepth := 0
		instruction := handoverSummaryInstruction
		text := source
		providerUsed := false
		for {
			if ctx.Err() != nil {
				break
			}
			next, err := p.summarize(ctx, text, instruction)
			if err == nil {
				next = strings.TrimSpace(next)
				if next != "" {
					summary = next
					providerUsed = true
				}
			}
			if len([]rune(summary)) <= handoverSummaryMaxChars || currentDepth >= maxDepth {
				break
			}
			text = summary
			instruction = fmt.Sprintf("Condense the handover report while preserving all critical findings and references. Round %d", currentDepth+1)
			currentDepth++
		}
		if providerUsed {
			sourceMeta = ContextSummarySourceProvider
			localFallback = false
		}
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = source
	}
	if len([]rune(summary)) > handoverSummaryMaxChars {
		summary = truncateRunes(summary, handoverSummaryMaxChars)
	}
	if strings.TrimSpace(sourceMeta) == "" {
		sourceMeta = ContextSummarySourceDeterministicFallback
		localFallback = true
	}
	return plannerSummaryResult{
		Text:          strings.TrimSpace(summary),
		Tokens:        estimateStringTokens(summary),
		Source:        sourceMeta,
		LocalFallback: localFallback,
	}
}

func (p *ContextPlanner) compressChunk(ctx context.Context, msgs []store.Message, maxDepth int, charsPerToken int) (plannerSummaryResult, error) {
	text := messagesText(msgs)
	base := planFallbackSummary(text)
	fallback := plannerSummaryResult{
		Text:          base,
		Tokens:        estimateStringTokens(base, charsPerToken),
		Source:        ContextSummarySourceDeterministicFallback,
		LocalFallback: true,
	}
	if p.summarize == nil {
		return fallback, nil
	}
	summary := strings.TrimSpace(base)
	instruction := handoverSummaryInstruction
	currentDepth := 0
	source := ContextSummarySourceDeterministicFallback
	localFallback := true
	for {
		if ctx.Err() != nil {
			return plannerSummaryResult{}, ctx.Err()
		}
		next, err := p.summarize(ctx, text, instruction)
		if err == nil {
			next = strings.TrimSpace(next)
			if next != "" {
				summary = next
				source = ContextSummarySourceProvider
				localFallback = false
			}
		}
		summaryTokens := estimateStringTokens(summary, charsPerToken)
		if summaryTokens <= defaultSummaryChunkTokens || currentDepth >= maxDepth {
			return plannerSummaryResult{
				Text:          summary,
				Tokens:        summaryTokens,
				Source:        source,
				LocalFallback: localFallback,
			}, nil
		}
		title := fmt.Sprintf("Compress further while preserving handover-critical findings, queries, and artifact references. Round %d", currentDepth+1)
		text = summary
		instruction = title
		currentDepth++
	}
}

func countReferencedArtifacts(msgs []store.Message) int {
	count := 0
	seen := map[string]struct{}{}
	for _, m := range msgs {
		for _, id := range m.ReferencedArtifactIDs {
			v := strings.TrimSpace(id)
			if v == "" {
				continue
			}
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			count++
		}
	}
	return count
}

func contextFromBuckets(buckets []ContextSummaryBucket) string {
	if len(buckets) == 0 {
		return ""
	}
	parts := make([]string, 0, len(buckets))
	for _, b := range buckets {
		if strings.TrimSpace(b.Summary) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(b.Summary))
	}
	return strings.Join(parts, " ")
}

func summarySourceFromBuckets(buckets []ContextSummaryBucket) string {
	source := ""
	for _, bucket := range buckets {
		source = mergeSummarySource(source, bucket.SummarySource)
	}
	return source
}

func bucketsUseLocalFallback(buckets []ContextSummaryBucket) bool {
	for _, bucket := range buckets {
		if bucket.LocalFallback || strings.EqualFold(strings.TrimSpace(bucket.SummarySource), ContextSummarySourceDeterministicFallback) {
			return true
		}
	}
	return false
}

func mergeSummarySource(current string, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if current == "" {
		return next
	}
	if next == "" || current == next {
		return current
	}
	if current == ContextSummarySourceMixed || next == ContextSummarySourceMixed {
		return ContextSummarySourceMixed
	}
	return ContextSummarySourceMixed
}

func buildRetrievalHints(selectedIDs []string, artifactRefs int, summaryContext string, hasBuckets bool) []ContextRetrievalHint {
	out := make([]ContextRetrievalHint, 0, 2)
	if len(selectedIDs) > 0 {
		h := ContextRetrievalHint{
			QueryHint:           "Retrieve older context for previous turns",
			CandidateMessageIDs: append([]string(nil), selectedIDs...),
			MinScore:            0.05,
			OrderBy:             "seq_desc",
		}
		if hasBuckets {
			h.QueryHint = "Retrieve summarized buckets then verify with list_messages/message_ids"
		}
		out = append(out, h)
	}
	if artifactRefs > 0 && strings.TrimSpace(summaryContext) != "" {
		out = append(out, ContextRetrievalHint{
			QueryHint: "Search artifacts referenced by current turn before falling back to full-message recall",
			MinScore:  0.10,
			OrderBy:   "score_desc",
		})
	}
	return out
}

func sanitizePolicy(in ContextPolicy) ContextPolicy {
	if in.MaxInputTokens <= 0 {
		in.MaxInputTokens = defaultMaxContextInputTokens
	}
	if in.HardContextCap <= 0 {
		in.HardContextCap = defaultHardContextTokenCap
	}
	if in.BaseWindowTurns <= 0 {
		in.BaseWindowTurns = defaultBaseWindowTurns
	}
	if in.SummaryChunkTokens <= 0 {
		in.SummaryChunkTokens = defaultSummaryChunkTokens
	}
	if in.MaxSummaryDepth <= 0 {
		in.MaxSummaryDepth = defaultMaxSummaryDepth
	}
	if in.NeedRetrievalLimit <= 0 {
		in.NeedRetrievalLimit = defaultSummaryNeedTokenLimit
	}
	if in.EvidenceBudget <= 0 {
		in.EvidenceBudget = 16
	}
	if in.CharsPerToken <= 0 {
		in.CharsPerToken = defaultTokenCharsPerToken
	}
	in.EnableSummary = true
	return in
}

func chunkMessages(msgs []store.Message, maxTokens int, charsPerToken ...int) [][]store.Message {
	if maxTokens <= 0 {
		maxTokens = defaultSummaryChunkTokens
	}
	if len(msgs) == 0 {
		return nil
	}
	cpt := resolveCharsPerToken(charsPerToken...)
	var out [][]store.Message
	bucket := []store.Message{}
	bucketTokens := 0
	for _, m := range msgs {
		t := estimateMessageTokens(m, cpt)
		if t > maxTokens && len(bucket) > 0 {
			out = append(out, append([]store.Message(nil), bucket...))
			bucket = nil
			bucketTokens = 0
		}
		if t > maxTokens {
			out = append(out, []store.Message{m})
			bucket = nil
			bucketTokens = 0
			continue
		}
		if bucketTokens+t > maxTokens && len(bucket) > 0 {
			out = append(out, append([]store.Message(nil), bucket...))
			bucket = nil
			bucketTokens = 0
		}
		bucket = append(bucket, m)
		bucketTokens += t
	}
	if len(bucket) > 0 {
		out = append(out, append([]store.Message(nil), bucket...))
	}
	if len(out) == 0 && len(msgs) > 0 {
		out = append(out, msgs)
	}
	return out
}

func summarizeBucketsText(buckets []ContextSummaryBucket) string {
	if len(buckets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Context Summary (older turns):\n")
	for i, bucket := range buckets {
		if i > 0 {
			b.WriteString("\n")
		}
		title := fmt.Sprintf("Bucket %d (seq %d-%d): ", i+1, bucket.FromSeq, bucket.ToSeq)
		b.WriteString(title)
		b.WriteString(strings.TrimSpace(bucket.Summary))
	}
	return b.String()
}

func messagesText(msgs []store.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(fmt.Sprintf("[%d %s] ", m.Seq, m.Role))
		if m.Content != nil {
			b.WriteString(strings.TrimSpace(*m.Content))
		}
		if len(m.ReferencedArtifactIDs) > 0 {
			b.WriteString(" refs=" + strings.Join(m.ReferencedArtifactIDs, ","))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func planFallbackSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return strings.TrimSpace(truncateRunes(text, 1000))
}

func toChatHistory(msgs []store.Message, userMsgID string, hiddenSummary string, _ string) []types.ChatMessage {
	out := make([]types.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		msg := types.ChatMessage{
			Role:            m.Role,
			FileAttachments: fileAttachmentRefsForMessage(m.Role, m.ReferencedArtifactIDs),
		}
		if m.Content != nil {
			msg.Content = m.Content
		}
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = m.ToolCalls
		}
		if m.Role == types.RoleTool {
			if m.ToolCallID != nil {
				msg.ToolCallID = strings.TrimSpace(*m.ToolCallID)
			}
			if m.Name != nil {
				msg.Name = strings.TrimSpace(*m.Name)
			}
			msg.Content = compactToolResultForModel(msg.Content)
		}
		if strings.TrimSpace(m.ID) == strings.TrimSpace(userMsgID) && msg.Content != nil {
			msg.Content = ptrString(combineHiddenParts(safeContent(msg.Content), hiddenSummary))
		}
		if m.Content == nil && strings.TrimSpace(m.ID) == strings.TrimSpace(userMsgID) && safeContentString(hiddenSummary) != "" {
			msg.Content = ptrString(hiddenSummary)
		}
		out = append(out, msg)
	}
	return out
}

func compactToolResultForModel(content *string) *string {
	if content == nil {
		return nil
	}
	raw := strings.TrimSpace(safeContent(content))
	if raw == "" {
		return nil
	}
	env, ok := types.ParseToolResultEnvelope(raw)
	if !ok {
		return ptrString(raw)
	}
	if !shouldCompactToolEnvelopeForModelHistory(env.ResultFormat, env.Result) {
		return ptrString(raw)
	}
	if compacted, compactedOK := types.CompactToolResultPayload(raw); compactedOK {
		return ptrString(strings.TrimSpace(compacted))
	}
	return ptrString(raw)
}

func shouldCompactToolEnvelopeForModelHistory(resultFormat string, result any) bool {
	if !strings.EqualFold(strings.TrimSpace(resultFormat), "json") {
		return false
	}
	if mm, ok := result.(map[string]any); ok {
		if _, _, exceeded := firstOversizedToolResultArray(mm); exceeded {
			return true
		}
	}
	return marshalJSONSize(result) > toolResultEnvelopeByteLimit
}

func estimateHistoryTokens(msgs []types.ChatMessage, charsPerToken ...int) int {
	cpt := resolveCharsPerToken(charsPerToken...)
	total := 0
	for _, m := range msgs {
		total += estimateStringTokens(safeContent(m.Content), cpt)
	}
	return total
}

func idsFromMessages(msgs []store.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if strings.TrimSpace(m.ID) != "" {
			out = append(out, m.ID)
		}
	}
	return out
}

func estimateMessagesTokens(msgs []store.Message, charsPerToken ...int) int {
	cpt := resolveCharsPerToken(charsPerToken...)
	total := 0
	for _, m := range msgs {
		total += estimateMessageTokens(m, cpt)
	}
	return total
}

func estimateMessageTokens(m store.Message, charsPerToken ...int) int {
	if m.Content == nil {
		return 0
	}
	return estimateStringTokens(*m.Content, charsPerToken...)
}

func estimateStringTokens(s string, charsPerToken ...int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	cpt := resolveCharsPerToken(charsPerToken...)
	return max(1, (len(s)+(cpt-1))/cpt)
}

func resolveCharsPerToken(charsPerToken ...int) int {
	cpt := defaultTokenCharsPerToken
	if len(charsPerToken) > 0 && charsPerToken[0] > 0 {
		cpt = charsPerToken[0]
	}
	return cpt
}

func combineHiddenParts(summary string, hidden string) string {
	sum := sanitizeTrim(summary)
	h := sanitizeTrim(hidden)
	if sum == "" {
		return h
	}
	if h == "" {
		return sum
	}
	return sum + "\n\n" + h
}

func sanitizeTrim(s string) string {
	return strings.TrimSpace(s)
}

func safeContent(c *string) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(*c)
}

func safeContentString(s string) string {
	return strings.TrimSpace(s)
}

func ptrString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateRunes(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxChars {
		return string(r)
	}
	return string(r[:maxChars])
}
