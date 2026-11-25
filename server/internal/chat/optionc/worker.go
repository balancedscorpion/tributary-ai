package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
)

type WorkerOptions struct {
	PollInterval time.Duration
	Usage        usage.Repository
}

// StartJobWorker starts a background goroutine that processes thread title/summary jobs.
// It is best-effort: failures are recorded in the jobs table and retried with backoff.
func StartJobWorker(ctx context.Context, st store.Store, pf *providers.Factory, opts *WorkerOptions) {
	if st == nil || pf == nil {
		return
	}
	pi := 750 * time.Millisecond
	if opts != nil && opts.PollInterval > 0 {
		pi = opts.PollInterval
	}
	var usageRepository usage.Repository
	if opts != nil {
		usageRepository = opts.Usage
	}
	go func() {
		t := time.NewTicker(pi)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				now := time.Now().UTC()
				job, err := st.ClaimNextJob(ctx, now)
				if err != nil {
					continue
				}
				if job == nil {
					continue
				}
				jobErr := processJob(ctx, st, pf, *job)
				if job.RunID != nil {
					refreshRunUsageRollup(ctx, st, usageRepository, *job.RunID)
				}
				if jobErr != nil {
					backoff := time.Duration(2*job.Attempts) * time.Second
					if backoff < 2*time.Second {
						backoff = 2 * time.Second
					}
					if backoff > 60*time.Second {
						backoff = 60 * time.Second
					}
					_ = st.FailJob(ctx, job.ID, jobErr.Error(), time.Now().UTC().Add(backoff))
					continue
				}
				_ = st.CompleteJob(ctx, job.ID)
			}
		}
	}()
}

func refreshRunUsageRollup(ctx context.Context, st store.Store, repository usage.Repository, runID string) {
	runID = strings.TrimSpace(runID)
	if st == nil || repository == nil || runID == "" {
		return
	}
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "completed", "failed", "cancelled":
	default:
		return
	}
	totals, err := repository.SumByRun(ctx, runID)
	if err != nil || totals.InvocationCount == 0 {
		return
	}
	fields := map[string]any{
		"input_tokens":  nil,
		"output_tokens": nil,
		"total_tokens":  nil,
	}
	if totals.CallsWithInputTokens == totals.InvocationCount {
		fields["input_tokens"] = totals.InputTokens
	}
	if totals.CallsWithOutputTokens == totals.InvocationCount {
		fields["output_tokens"] = totals.OutputTokens
	}
	if totals.CallsWithTotalTokens == totals.InvocationCount {
		fields["total_tokens"] = totals.TotalTokens
	}
	_ = st.UpdateRunStatus(ctx, run.ID, run.Status, fields)
}

func processJob(ctx context.Context, st store.Store, pf *providers.Factory, job store.Job) error {
	switch strings.TrimSpace(job.Kind) {
	case "thread_title":
		ctx = usage.WithAttribution(ctx, usage.Attribution{
			RunID: optionalUsageRunID(job.RunID), ThreadID: job.ThreadID, JobID: job.ID, Operation: usage.OperationThreadTitle,
		})
		return runTitleJob(ctx, st, pf, job.ThreadID)
	case "thread_summarize":
		ctx = usage.WithAttribution(ctx, usage.Attribution{
			RunID: optionalUsageRunID(job.RunID), ThreadID: job.ThreadID, JobID: job.ID, Operation: usage.OperationThreadSummary,
		})
		return runSummaryJob(ctx, st, pf, job.ThreadID)
	default:
		return fmt.Errorf("unknown job kind: %s", job.Kind)
	}
}

func optionalUsageRunID(runID *string) string {
	if runID == nil {
		return ""
	}
	return strings.TrimSpace(*runID)
}

func runTitleJob(ctx context.Context, st store.Store, pf *providers.Factory, threadID string) error {
	thread, err := st.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	// Skip if already named.
	if strings.TrimSpace(thread.Title) != "" && !strings.EqualFold(strings.TrimSpace(thread.Title), "New Thread") {
		return nil
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{ThreadID: threadID, Limit: 40})
	if err != nil {
		return err
	}
	txt := formatRecentMessages(msgs, 12)

	prompt := strings.Join([]string{
		"Return strict JSON with no surrounding text.",
		"Task: generate a short, descriptive thread title (<= 60 characters).",
		"JSON schema: {\"title\":\"...\"}",
		"",
		"Recent messages:",
		txt,
	}, "\n")

	out, err := callModelJSON(ctx, pf, prompt)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(out.Title)
	if title == "" {
		return errors.New("model returned empty title")
	}
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60])
	}
	return st.UpdateThreadTitleSummary(ctx, threadID, &title, nil)
}

func runSummaryJob(ctx context.Context, st store.Store, pf *providers.Factory, threadID string) error {
	thread, err := st.GetThread(ctx, threadID)
	if err != nil {
		return err
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{ThreadID: threadID, Limit: 80})
	if err != nil {
		return err
	}
	txt := formatRecentMessages(msgs, 24)

	cur := ""
	if thread.Summary != nil {
		cur = strings.TrimSpace(*thread.Summary)
	}

	prompt := strings.Join([]string{
		"Return strict JSON with no surrounding text.",
		"Task: update a rolling thread summary (<= 1500 characters).",
		"Keep it factual and compact. Prefer bullet-like sentences separated by newlines.",
		"JSON schema: {\"summary\":\"...\"}",
		"",
		"Current summary (may be empty):",
		cur,
		"",
		"Recent messages:",
		txt,
	}, "\n")

	out, err := callModelJSON(ctx, pf, prompt)
	if err != nil {
		return err
	}
	summary := strings.TrimSpace(out.Summary)
	if summary == "" {
		return errors.New("model returned empty summary")
	}
	if len([]rune(summary)) > 1500 {
		summary = string([]rune(summary)[:1500])
	}
	return st.UpdateThreadTitleSummary(ctx, threadID, nil, &summary)
}

type modelJSON struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

func callModelJSON(ctx context.Context, pf *providers.Factory, userPrompt string) (modelJSON, error) {
	prov, err := pf.CreateProvider("")
	if err != nil {
		return modelJSON{}, err
	}
	sys := strings.Join([]string{
		"You are a summarization helper for a local chat application.",
		"Never call tools.",
		"Output must be strict JSON only.",
	}, "\n")
	msg := types.ChatMessage{Role: types.RoleUser, Content: ptr(userPrompt)}
	ch, err := prov.StreamChat(ctx, types.ChatRequest{
		Model:        prov.Model(),
		SystemPrompt: sys,
		Messages:     []types.ChatMessage{msg},
		Tools:        nil,
	})
	if err != nil {
		return modelJSON{}, err
	}

	var accum strings.Builder
	for ev := range ch {
		switch ev.Event {
		case types.EventTextDelta:
			if d, ok := ev.Data.(types.TextDeltaData); ok {
				accum.WriteString(d.Content)
			}
		case types.EventFinal:
			if d, ok := ev.Data.(types.FinalData); ok && strings.TrimSpace(d.Answer) != "" {
				accum.Reset()
				accum.WriteString(d.Answer)
			}
		case types.EventError:
			if d, ok := ev.Data.(types.ErrorData); ok {
				return modelJSON{}, errors.New(d.Message)
			}
		case types.EventToolCalls:
			return modelJSON{}, errors.New("model attempted to call tools during summarization")
		}
	}

	raw := strings.TrimSpace(accum.String())
	if raw == "" {
		return modelJSON{}, errors.New("empty model output")
	}
	objText := extractJSONObject(raw)
	var out modelJSON
	if err := json.Unmarshal([]byte(objText), &out); err != nil {
		return modelJSON{}, fmt.Errorf("invalid JSON output: %w", err)
	}
	return out, nil
}

func extractJSONObject(s string) string {
	// Best-effort: strip markdown fences, then take the first {...} block.
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func formatRecentMessages(msgs []store.Message, max int) string {
	// Filter UI-hidden and include last max messages.
	out := make([]store.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.UIHidden {
			continue
		}
		// Skip tool messages (they tend to be verbose); keep assistant+user for summary/title.
		if m.Role == types.RoleTool {
			continue
		}
		out = append(out, m)
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	var b strings.Builder
	for _, m := range out {
		role := string(m.Role)
		content := ""
		if m.Content != nil {
			content = strings.TrimSpace(*m.Content)
		}
		if content == "" {
			continue
		}
		if len(content) > 800 {
			content = content[:800] + "..."
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(content)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func ptr(s string) *string { return &s }
