package rlm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/chat/messagepaging"
	"ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type Evidence struct {
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
}

var (
	rlmOpenCallPattern   = regexp.MustCompile(`(^|[^a-zA-Z0-9_])open\s*\(`)
	rlmReadFileCall      = regexp.MustCompile(`(^|[^a-zA-Z0-9_])read_file\s*\(`)
	rlmFStringPattern    = regexp.MustCompile(`(^|[^a-zA-Z0-9_])f["']`)
	rlmDirNoArgPattern   = regexp.MustCompile(`\bdir\s*\(\s*\)`)
	rlmMaxResultsPattern = regexp.MustCompile(`\bmax_results\s*=`)
)

func addValidationIssue(issues *[]string, seen map[string]struct{}, key, msg string) {
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*issues = append(*issues, msg)
}

func validateStarlarkCode(code string) error {
	issues := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, rawLine := range strings.Split(code, "\n") {
		line := strings.TrimSpace(rawLine)
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}
		if rlmOpenCallPattern.MatchString(line) {
			addValidationIssue(&issues, seen, "open", "open() is not supported in rlm_exec. Use allowed Starlark helpers (search/get_artifact/list_messages/get_doc) and pass artifact IDs from earlier tool results.")
		}
		if rlmReadFileCall.MatchString(line) {
			addValidationIssue(&issues, seen, "read_file", "read_file() is not available in rlm_exec. Read files via external read_file tool first, then load needed artifact IDs through get_artifact().")
		}
		if rlmFStringPattern.MatchString(line) {
			addValidationIssue(&issues, seen, "fstring", "f-strings are not valid Starlark syntax. Use plain string concatenation or formatting via + and str().")
		}
		if rlmDirNoArgPattern.MatchString(line) {
			addValidationIssue(&issues, seen, "dir_no_args", "dir() requires an argument in this runtime; use dir(value) and inspect object keys safely.")
		}
		if rlmMaxResultsPattern.MatchString(line) {
			addValidationIssue(&issues, seen, "max_results", "search() does not accept max_results. Use limit=<int> for result count.")
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("rlm_exec hardening check failed: %s", strings.Join(issues, " "))
}

func ValidateCode(code string) error {
	clean := strings.TrimSpace(code)
	if clean == "" {
		return errors.New("code is required")
	}
	if err := validateStarlarkCode(clean); err != nil {
		return err
	}
	var opts syntax.FileOptions
	if _, err := opts.Parse("rlm_exec.star", clean, 0); err != nil {
		return fmt.Errorf("starlark syntax error: %w", err)
	}
	return nil
}

type SubLLMFunc func(ctx context.Context, text string, instruction string, modelOverride string) (string, error)

type ExecRequest struct {
	Code           string
	Note           string
	Depth          string
	EvidenceBudget int
}

type ExecResult struct {
	OK                      bool       `json:"ok"`
	StdoutPrefix            string     `json:"stdout_prefix"`
	StdoutLen               int        `json:"stdout_len"`
	PromptLen               int        `json:"prompt_len"`
	Evidence                []Evidence `json:"evidence"`
	Done                    bool       `json:"done"`
	EvidenceBudget          int        `json:"evidence_budget,omitempty"`
	EvidenceUsed            int        `json:"evidence_used"`
	EvidenceBudgetExhausted bool       `json:"evidence_budget_exhausted"`
	FinalAnswer             *string    `json:"final_answer,omitempty"`
	LastOpsSummary          string     `json:"last_ops_summary,omitempty"`
	Error                   string     `json:"error,omitempty"`
}

func (r ExecResult) Preview() map[string]any {
	out := map[string]any{
		"ok":             r.OK,
		"done":           r.Done,
		"prompt_len":     r.PromptLen,
		"stdout_len":     r.StdoutLen,
		"evidence_count": len(r.Evidence),
		"evidence_used":  r.EvidenceUsed,
	}

	if r.StdoutPrefix != "" {
		out["stdout_prefix"] = truncateRunes(strings.TrimSpace(r.StdoutPrefix), 1024)
	}
	if r.Error != "" {
		out["error"] = strings.TrimSpace(r.Error)
	}
	if r.EvidenceBudget > 0 {
		out["evidence_budget"] = r.EvidenceBudget
	}
	if r.EvidenceBudgetExhausted {
		out["evidence_budget_exhausted"] = true
	}
	if r.LastOpsSummary != "" {
		out["last_ops_summary"] = strings.TrimSpace(r.LastOpsSummary)
	}
	if r.FinalAnswer != nil {
		if trimmed := strings.TrimSpace(*r.FinalAnswer); trimmed != "" {
			out["final_answer"] = trimmed
		}
	}

	evidenceLimit := 8
	if len(r.Evidence) > 0 {
		limit := evidenceLimit
		if len(r.Evidence) < limit {
			limit = len(r.Evidence)
		}
		preview := make([]Evidence, 0, limit)
		for i := 0; i < limit; i++ {
			preview = append(preview, r.Evidence[i])
		}
		out["evidence_preview"] = preview
		if len(r.Evidence) > limit {
			out["evidence_truncated"] = true
			out["evidence_total"] = len(r.Evidence)
		}
	}

	return out
}

func truncateRunes(raw string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(raw)
	if len(runes) <= max {
		return raw
	}
	return string(runes[:max]) + "... [truncated]"
}

type Options struct {
	Search *search.Service
	Store  store.Store
	CfgDir string
	SubLLM SubLLMFunc

	MaxExecutionSteps uint64
	MaxStdoutBytes    int
	MaxPromptBytes    int
	MaxCodeBytes      int
	MaxSubCalls       int
	MaxRecursionDepth int
}

type Runtime struct {
	search *search.Service
	store  store.Store
	cfgDir string
	subLLM SubLLMFunc

	maxExecutionSteps uint64
	maxStdoutBytes    int
	maxPromptBytes    int
	maxCodeBytes      int
	maxSubCalls       int
	maxRecursionDepth int

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	prompt string

	evidence                []Evidence
	evSeen                  map[string]int
	evidenceBudget          int
	evidenceUsed            int
	evidenceBudgetExhausted bool

	done       bool
	final      string
	subCalls   int
	curDepth   int
	lastOpNote string
}

func NewRuntime(opts Options) *Runtime {
	maxSteps := opts.MaxExecutionSteps
	if maxSteps == 0 {
		maxSteps = 80_000
	}
	maxStdout := opts.MaxStdoutBytes
	if maxStdout <= 0 {
		maxStdout = 64 << 10
	}
	maxPrompt := opts.MaxPromptBytes
	if maxPrompt <= 0 {
		maxPrompt = 2 << 20
	}
	maxCode := opts.MaxCodeBytes
	if maxCode <= 0 {
		maxCode = 64 << 10
	}
	maxSub := opts.MaxSubCalls
	if maxSub <= 0 {
		maxSub = 8
	}
	maxDepth := opts.MaxRecursionDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}
	return &Runtime{
		search:            opts.Search,
		store:             opts.Store,
		cfgDir:            strings.TrimSpace(opts.CfgDir),
		subLLM:            opts.SubLLM,
		maxExecutionSteps: maxSteps,
		maxStdoutBytes:    maxStdout,
		maxPromptBytes:    maxPrompt,
		maxCodeBytes:      maxCode,
		maxSubCalls:       maxSub,
		maxRecursionDepth: maxDepth,
		sessions:          map[string]*session{},
	}
}

func (r *Runtime) Clear(runID string) {
	id := strings.TrimSpace(runID)
	if id == "" {
		return
	}
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

func (r *Runtime) Exec(ctx context.Context, runID string, req ExecRequest) (ExecResult, error) {
	if r == nil {
		return ExecResult{OK: false, Error: "runtime is nil"}, errors.New("runtime is nil")
	}
	id := strings.TrimSpace(runID)
	if id == "" {
		return ExecResult{OK: false, Error: "run id is required"}, errors.New("run id is required")
	}
	code := strings.TrimSpace(req.Code)
	if len(code) > r.maxCodeBytes {
		return ExecResult{OK: false, Error: "code exceeds max size"}, nil
	}
	if err := ValidateCode(code); err != nil {
		return ExecResult{OK: false, Error: err.Error()}, nil
	}

	s := r.getOrCreateSession(id)
	if req.EvidenceBudget > 0 {
		if s.evidenceBudget == 0 {
			s.evidenceBudget = req.EvidenceBudget
			s.evidenceUsed = 0
			s.evidenceBudgetExhausted = false
		}
	}
	var out strings.Builder
	wrote := 0
	thread := &starlark.Thread{
		Name: "rlm_exec",
		Print: func(_ *starlark.Thread, msg string) {
			if wrote >= r.maxStdoutBytes {
				return
			}
			remaining := r.maxStdoutBytes - wrote
			if remaining <= 0 {
				return
			}
			if len(msg) > remaining {
				msg = msg[:remaining]
			}
			out.WriteString(msg)
			out.WriteByte('\n')
			wrote += len(msg) + 1
		},
	}
	thread.SetMaxExecutionSteps(r.maxExecutionSteps)

	predeclared := starlark.StringDict{
		"prompt_len":    starlark.NewBuiltin("prompt_len", r.promptLenBuiltin(s)),
		"prompt_peek":   starlark.NewBuiltin("prompt_peek", r.promptPeekBuiltin(s)),
		"prompt_slice":  starlark.NewBuiltin("prompt_slice", r.promptSliceBuiltin(s)),
		"set_prompt":    starlark.NewBuiltin("set_prompt", r.setPromptBuiltin(s)),
		"append_prompt": starlark.NewBuiltin("append_prompt", r.appendPromptBuiltin(s)),
		"clear_prompt":  starlark.NewBuiltin("clear_prompt", r.clearPromptBuiltin(s)),
		"search":        starlark.NewBuiltin("search", r.searchBuiltin(ctx, s)),
		"get_doc":       starlark.NewBuiltin("get_doc", r.getDocBuiltin(ctx, s)),
		"get_artifact":  starlark.NewBuiltin("get_artifact", r.getArtifactBuiltin(ctx, s)),
		"list_messages": starlark.NewBuiltin("list_messages", r.listMessagesBuiltin(ctx, s)),
		"add_evidence":  starlark.NewBuiltin("add_evidence", r.addEvidenceBuiltin(s)),
		"set_final":     starlark.NewBuiltin("set_final", r.setFinalBuiltin(s)),
		"finish":        starlark.NewBuiltin("finish", r.finishBuiltin(s)),
		"sub_llm":       starlark.NewBuiltin("sub_llm", r.subLLMBuiltin(ctx, s)),
		"sub_rlm":       starlark.NewBuiltin("sub_rlm", r.subRLMBuiltin(ctx, s)),
	}

	_, execErr := starlark.ExecFile(thread, "rlm_exec.star", code, predeclared)
	result := ExecResult{
		OK:                      execErr == nil,
		StdoutPrefix:            out.String(),
		StdoutLen:               len(out.String()),
		PromptLen:               len(s.prompt),
		Evidence:                append([]Evidence(nil), s.evidence...),
		Done:                    s.done,
		EvidenceBudget:          s.evidenceBudget,
		EvidenceUsed:            s.evidenceUsed,
		EvidenceBudgetExhausted: s.evidenceBudgetExhausted,
	}
	if strings.TrimSpace(s.final) != "" {
		final := strings.TrimSpace(s.final)
		result.FinalAnswer = &final
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		note = strings.TrimSpace(s.lastOpNote)
	}
	if note != "" {
		result.LastOpsSummary = note
	} else {
		result.LastOpsSummary = fmt.Sprintf("prompt_len=%d evidence=%d done=%v", len(s.prompt), len(s.evidence), s.done)
	}
	if execErr != nil {
		result.Error = execErr.Error()
		result.OK = false
	}
	return result, nil
}

func (r *Runtime) getOrCreateSession(runID string) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[runID]; ok {
		return s
	}
	s := &session{
		evSeen: map[string]int{},
	}
	r.sessions[runID] = s
	return s
}

func (r *Runtime) promptLenBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		return starlark.MakeInt(len(s.prompt)), nil
	}
}

func (r *Runtime) promptPeekBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		n := 200
		if err := starlark.UnpackArgs("prompt_peek", args, nil, "n?", &n); err != nil {
			return nil, err
		}
		if n < 0 {
			n = 0
		}
		if n > len(s.prompt) {
			n = len(s.prompt)
		}
		return starlark.String(s.prompt[:n]), nil
	}
}

func (r *Runtime) promptSliceBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		start := 0
		end := len(s.prompt)
		if err := starlark.UnpackArgs("prompt_slice", args, nil, "start", &start, "end", &end); err != nil {
			return nil, err
		}
		if start < 0 {
			start = 0
		}
		if end < start {
			end = start
		}
		if start > len(s.prompt) {
			start = len(s.prompt)
		}
		if end > len(s.prompt) {
			end = len(s.prompt)
		}
		return starlark.String(s.prompt[start:end]), nil
	}
}

func (r *Runtime) setPromptBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		text := ""
		if err := starlark.UnpackArgs("set_prompt", args, nil, "text", &text); err != nil {
			return nil, err
		}
		if len(text) > r.maxPromptBytes {
			text = text[:r.maxPromptBytes]
		}
		s.prompt = text
		return starlark.None, nil
	}
}

func (r *Runtime) appendPromptBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		text := ""
		if err := starlark.UnpackArgs("append_prompt", args, nil, "text", &text); err != nil {
			return nil, err
		}
		if text == "" {
			return starlark.None, nil
		}
		next := s.prompt + text
		if len(next) > r.maxPromptBytes {
			next = next[:r.maxPromptBytes]
		}
		s.prompt = next
		return starlark.None, nil
	}
}

func (r *Runtime) clearPromptBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		s.prompt = ""
		return starlark.None, nil
	}
}

func (r *Runtime) searchBuiltin(ctx context.Context, s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		if r.search == nil {
			return nil, errors.New("search service is not configured")
		}
		query := ""
		limit := 10
		status := ""
		projectID := ""
		minScore := 0.0
		maxChars := 0
		orderBy := ""
		var beforeTSStr, afterTSStr starlark.String
		var threadIDs *starlark.List
		var artifactIDs *starlark.List
		var messageIDs *starlark.List
		if err := starlark.UnpackArgs("search", args, nil,
			"q", &query,
			"limit?", &limit,
			"status?", &status,
			"project_id?", &projectID,
			"thread_ids?", &threadIDs,
			"artifact_ids?", &artifactIDs,
			"message_ids?", &messageIDs,
			"before_ts?", &beforeTSStr,
			"after_ts?", &afterTSStr,
			"min_score?", &minScore,
			"order_by?", &orderBy,
			"max_chars?", &maxChars,
		); err != nil {
			return nil, err
		}
		if err := r.consumeEvidenceBudget(s, "search"); err != nil {
			return nil, err
		}
		queryText := strings.TrimSpace(query)
		if queryText == "" {
			return nil, errors.New("q is required")
		}
		statusText := strings.TrimSpace(status)
		if statusText != "" && statusText != "active" && statusText != "archived" && statusText != "*" {
			return nil, errors.New("status must be one of: active, archived, *")
		}
		rawOrderBy := strings.TrimSpace(orderBy)
		orderBy = normalizeSearchDocsOrderBy(rawOrderBy)
		if orderBy == "" && rawOrderBy != "" {
			return nil, errors.New("order_by must be one of: score_desc, updated_at_desc, updated_at_asc")
		}
		if minScore < 0 {
			return nil, errors.New("min_score must be >= 0")
		}
		if minScore > 1 {
			return nil, errors.New("min_score must be <= 1")
		}
		if limit <= 0 {
			limit = 10
		}
		if limit > 25 {
			return nil, errors.New("limit must be <= 25")
		}
		if maxChars < 0 {
			return nil, errors.New("max_chars must be >= 0")
		}
		if maxChars > 500 {
			return nil, errors.New("max_chars must be <= 500")
		}
		if maxChars == 0 {
			maxChars = 320
		}

		threadIDList, err := toStringSlice("thread_ids", threadIDs)
		if err != nil {
			return nil, err
		}
		if len(threadIDList) > 100 {
			return nil, errors.New("thread_ids length must be <= 100")
		}
		artifactIDList, err := toStringSlice("artifact_ids", artifactIDs)
		if err != nil {
			return nil, err
		}
		if len(artifactIDList) > 100 {
			return nil, errors.New("artifact_ids length must be <= 100")
		}
		messageIDList, err := toStringSlice("message_ids", messageIDs)
		if err != nil {
			return nil, err
		}
		if len(messageIDList) > 100 {
			return nil, errors.New("message_ids length must be <= 100")
		}

		var beforeTS *time.Time
		if strings.TrimSpace(string(beforeTSStr)) != "" {
			parsed, parseErr := parseRFC3339Timestamp(string(beforeTSStr))
			if parseErr != nil {
				return nil, errors.New("before_ts must be RFC3339 timestamp")
			}
			beforeTS = parsed
		}
		var afterTS *time.Time
		if strings.TrimSpace(string(afterTSStr)) != "" {
			parsed, parseErr := parseRFC3339Timestamp(string(afterTSStr))
			if parseErr != nil {
				return nil, errors.New("after_ts must be RFC3339 timestamp")
			}
			afterTS = parsed
		}
		if beforeTS != nil && afterTS != nil && beforeTS.Before(*afterTS) {
			return nil, errors.New("before_ts must be greater than or equal to after_ts")
		}

		_, results, err := r.search.Search(ctx, search.Query{
			Text:        queryText,
			Limit:       limit,
			Status:      statusText,
			ProjectID:   strings.TrimSpace(projectID),
			ThreadIDs:   threadIDList,
			ArtifactIDs: artifactIDList,
			MessageIDs:  messageIDList,
			BeforeTS:    beforeTS,
			AfterTS:     afterTS,
			MinScore:    minScore,
			OrderBy:     orderBy,
			Intent:      "keyword",
		})
		if err != nil {
			return nil, err
		}
		out := make([]starlark.Value, 0, len(results))
		for _, item := range results {
			snippet := item.Snippet
			if len(snippet) > maxChars {
				snippet = snippet[:maxChars]
			}
			d := starlark.NewDict(10)
			_ = d.SetKey(starlark.String("doc_id"), starlark.String(item.DocID))
			_ = d.SetKey(starlark.String("doc_kind"), starlark.String(item.DocKind))
			_ = d.SetKey(starlark.String("thread_id"), starlark.String(item.ThreadID))
			_ = d.SetKey(starlark.String("title"), starlark.String(item.Title))
			_ = d.SetKey(starlark.String("snippet"), starlark.String(snippet))
			_ = d.SetKey(starlark.String("score"), starlark.Float(item.Score))
			if item.RunID != nil {
				_ = d.SetKey(starlark.String("run_id"), starlark.String(*item.RunID))
			}
			if item.MessageID != nil {
				_ = d.SetKey(starlark.String("message_id"), starlark.String(*item.MessageID))
			}
			if item.ArtifactID != nil {
				_ = d.SetKey(starlark.String("artifact_id"), starlark.String(*item.ArtifactID))
			}
			out = append(out, d)
		}
		return starlark.NewList(out), nil
	}
}

func (r *Runtime) getDocBuiltin(ctx context.Context, s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		if r.search == nil {
			return nil, errors.New("search service is not configured")
		}
		docID := ""
		maxChars := 4000
		if err := starlark.UnpackArgs("get_doc", args, nil, "doc_id", &docID, "max_chars?", &maxChars); err != nil {
			return nil, err
		}
		if err := r.consumeEvidenceBudget(s, "get_doc"); err != nil {
			return nil, err
		}
		docID = strings.TrimSpace(docID)
		if docID == "" {
			return nil, errors.New("doc_id is required")
		}
		if maxChars < 0 {
			return nil, errors.New("max_chars must be >= 0")
		}
		if maxChars == 0 {
			maxChars = 4000
		}
		if maxChars > 20000 {
			return nil, errors.New("max_chars must be <= 20000")
		}
		doc, ok := r.search.GetDoc(docID)
		if !ok {
			return nil, errors.New("doc not found")
		}
		text := doc.Text
		if len(text) > maxChars {
			text = text[:maxChars]
		}
		d := starlark.NewDict(12)
		_ = d.SetKey(starlark.String("doc_id"), starlark.String(doc.ID))
		_ = d.SetKey(starlark.String("doc_kind"), starlark.String(doc.DocKind))
		_ = d.SetKey(starlark.String("thread_id"), starlark.String(doc.ThreadID))
		_ = d.SetKey(starlark.String("title"), starlark.String(doc.Title))
		_ = d.SetKey(starlark.String("text"), starlark.String(text))
		if doc.RunID != nil {
			_ = d.SetKey(starlark.String("run_id"), starlark.String(*doc.RunID))
		}
		if doc.MessageID != nil {
			_ = d.SetKey(starlark.String("message_id"), starlark.String(*doc.MessageID))
		}
		if doc.ArtifactID != nil {
			_ = d.SetKey(starlark.String("artifact_id"), starlark.String(*doc.ArtifactID))
		}
		return d, nil
	}
}

func (r *Runtime) getArtifactBuiltin(ctx context.Context, s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		if r.store == nil {
			return nil, errors.New("store is not configured")
		}
		artifactID := ""
		maxBytes := 64 << 10
		includeImageData := false
		if err := starlark.UnpackArgs("get_artifact", args, nil, "artifact_id", &artifactID, "max_bytes?", &maxBytes, "include_image_data?", &includeImageData); err != nil {
			return nil, err
		}
		artifactID = strings.TrimSpace(artifactID)
		if artifactID == "" {
			return nil, errors.New("artifact_id is required")
		}
		if err := r.consumeEvidenceBudget(s, "get_artifact"); err != nil {
			return nil, err
		}
		if maxBytes < 0 {
			return nil, errors.New("max_bytes must be >= 0")
		}
		if maxBytes == 0 {
			maxBytes = 64 << 10
		}
		if maxBytes > 256<<10 {
			return nil, errors.New("max_bytes must be <= 262144")
		}
		a, err := r.store.GetArtifact(ctx, strings.TrimSpace(artifactID))
		if err != nil {
			return nil, err
		}
		content := ""
		if strings.TrimSpace(a.Storage) == "file" && a.ContentPath != nil {
			content, _ = readArtifactFile(r.cfgDir, *a.ContentPath, int64(maxBytes))
		} else if a.ContentText != nil {
			content = *a.ContentText
		}
		if len(content) > maxBytes {
			content = content[:maxBytes]
		}
		imageContentOmitted := strings.EqualFold(strings.TrimSpace(a.Type), "image") && !includeImageData
		if imageContentOmitted {
			content = ""
		}
		d := starlark.NewDict(10)
		_ = d.SetKey(starlark.String("id"), starlark.String(a.ID))
		_ = d.SetKey(starlark.String("thread_id"), starlark.String(a.ThreadID))
		_ = d.SetKey(starlark.String("type"), starlark.String(a.Type))
		_ = d.SetKey(starlark.String("title"), starlark.String(a.Title))
		_ = d.SetKey(starlark.String("content"), starlark.String(content))
		_ = d.SetKey(starlark.String("byte_size"), starlark.MakeInt64(a.ByteSize))
		if imageContentOmitted {
			_ = d.SetKey(starlark.String("image_content_omitted"), starlark.True)
		}
		return d, nil
	}
}

func (r *Runtime) listMessagesBuiltin(ctx context.Context, s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if r.store == nil {
			return nil, errors.New("store is not configured")
		}
		threadID := ""
		limit := 20
		pageToken := ""
		withPageInfo := false
		before := starlark.Value(starlark.None)
		after := starlark.Value(starlark.None)
		var beforeTSStr, afterTSStr starlark.String
		var messageIDs *starlark.List
		var artifactIDs *starlark.List
		orderBy := ""
		maxChars := 0
		includeHidden := false
		if err := starlark.UnpackArgs("list_messages", args, kwargs,
			"thread_id", &threadID,
			"before_seq?", &before,
			"after_seq?", &after,
			"before_ts?", &beforeTSStr,
			"after_ts?", &afterTSStr,
			"message_ids?", &messageIDs,
			"artifact_ids?", &artifactIDs,
			"order_by?", &orderBy,
			"limit?", &limit,
			"include_hidden?", &includeHidden,
			"max_chars?", &maxChars,
			"page_token?", &pageToken,
			"with_page_info?", &withPageInfo,
		); err != nil {
			return nil, err
		}
		threadID = strings.TrimSpace(threadID)
		if threadID == "" {
			return nil, errors.New("thread_id is required")
		}
		var beforeSeq *int64
		var afterSeq *int64
		var beforeTS, afterTS *time.Time
		var messageIDList, artifactIDList []string
		var cursor messagepaging.Cursor
		pageToken = strings.TrimSpace(pageToken)
		if pageToken != "" {
			if before != starlark.None || after != starlark.None || strings.TrimSpace(string(beforeTSStr)) != "" || strings.TrimSpace(string(afterTSStr)) != "" ||
				(messageIDs != nil && messageIDs.Len() > 0) || (artifactIDs != nil && artifactIDs.Len() > 0) || strings.TrimSpace(orderBy) != "" {
				return nil, errors.New("page_token cannot be combined with sequence, timestamp, id, or order filters; copy it with only thread_id and page_token")
			}
			decoded, tokenErr := messagepaging.Decode(pageToken)
			if tokenErr != nil {
				return nil, tokenErr
			}
			if strings.TrimSpace(decoded.ThreadID) != threadID {
				return nil, errors.New("page_token belongs to a different thread")
			}
			cursor = decoded
			beforeSeq = cursor.BeforeSeq
			afterSeq = cursor.AfterSeq
			beforeTS, _ = parseRFC3339TimestampIfAny(cursor.BeforeTS)
			afterTS, _ = parseRFC3339TimestampIfAny(cursor.AfterTS)
			messageIDList = append([]string(nil), cursor.MessageIDs...)
			artifactIDList = append([]string(nil), cursor.ArtifactIDs...)
			orderBy = cursor.OrderBy
			limit = cursor.Limit
			maxChars = cursor.MaxChars
			includeHidden = cursor.IncludeHidden
		} else {
			if limit <= 0 {
				limit = 20
			}
			if limit > 100 {
				return nil, errors.New("limit must be <= 100")
			}
			if before != starlark.None {
				beforeInt, err := asInt64(before)
				if err != nil {
					return nil, errors.New("before_seq must be an integer")
				}
				if beforeInt == nil || *beforeInt < 0 {
					return nil, errors.New("before_seq must be >= 0")
				}
				beforeSeq = beforeInt
			}
			if after != starlark.None {
				afterInt, err := asInt64(after)
				if err != nil {
					return nil, errors.New("after_seq must be an integer")
				}
				if afterInt == nil || *afterInt < 0 {
					return nil, errors.New("after_seq must be >= 0")
				}
				afterSeq = afterInt
			}
			if beforeSeq != nil && afterSeq != nil && *afterSeq >= *beforeSeq {
				return nil, fmt.Errorf("before_seq must be greater than after_seq (before_seq=%d, after_seq=%d); use list_messages(..., with_page_info=True) and copy next_page_token", *beforeSeq, *afterSeq)
			}
			var err error
			beforeTS, err = parseRFC3339TimestampIfAny(string(beforeTSStr))
			if err != nil {
				return nil, errors.New("before_ts must be RFC3339 timestamp")
			}
			afterTS, err = parseRFC3339TimestampIfAny(string(afterTSStr))
			if err != nil {
				return nil, err
			}
			if beforeTS != nil && afterTS != nil && beforeTS.Before(*afterTS) {
				return nil, errors.New("before_ts must be greater than or equal to after_ts")
			}
			if maxChars < 0 {
				return nil, errors.New("max_chars_per_result must be >= 0")
			}
			if maxChars == 0 {
				maxChars = 3000
			}
			if maxChars > 3000 {
				return nil, errors.New("max_chars_per_result must be <= 3000")
			}
			rawOrderBy := strings.TrimSpace(orderBy)
			orderBy = normalizeListMessagesOrderBy(rawOrderBy)
			if orderBy == "" && rawOrderBy != "" {
				return nil, errors.New("order_by must be one of: seq_desc, seq_asc")
			}
			messageIDList, err = toStringSlice("message_ids", messageIDs)
			if err != nil {
				return nil, err
			}
			if len(messageIDList) > 100 {
				return nil, errors.New("message_ids length must be <= 100")
			}
			artifactIDList, err = toStringSlice("artifact_ids", artifactIDs)
			if err != nil {
				return nil, err
			}
			if len(artifactIDList) > 100 {
				return nil, errors.New("artifact_ids length must be <= 100")
			}
			sort.Strings(messageIDList)
			sort.Strings(artifactIDList)
			cursor = messagepaging.Cursor{
				ThreadID: threadID, OrderBy: orderBy, BeforeSeq: beforeSeq, AfterSeq: afterSeq,
				MessageIDs: messageIDList, ArtifactIDs: artifactIDList, Limit: limit, MaxChars: maxChars,
				IncludeHidden: includeHidden,
			}
			if beforeTS != nil {
				cursor.BeforeTS = beforeTS.UTC().Format(time.RFC3339Nano)
			}
			if afterTS != nil {
				cursor.AfterTS = afterTS.UTC().Format(time.RFC3339Nano)
			}
		}
		// Invalid calls do not consume the bounded evidence budget. Charge only
		// once the request is valid and is about to read from storage.
		if err := r.consumeEvidenceBudget(s, "list_messages"); err != nil {
			return nil, err
		}
		msgs, err := r.store.ListMessages(ctx, store.ListMessagesParams{
			ThreadID:      threadID,
			Limit:         limit + 1,
			BeforeSeq:     beforeSeq,
			AfterSeq:      afterSeq,
			BeforeTS:      beforeTS,
			AfterTS:       afterTS,
			MessageIDs:    messageIDList,
			ArtifactIDs:   artifactIDList,
			IncludeHidden: includeHidden,
			OrderBy:       strings.TrimSpace(orderBy),
		})
		if err != nil {
			return nil, err
		}
		hasMore := len(msgs) > limit
		if hasMore {
			msgs = msgs[:limit]
		}
		out := make([]starlark.Value, 0, len(msgs))
		for _, m := range msgs {
			d := starlark.NewDict(12)
			_ = d.SetKey(starlark.String("id"), starlark.String(m.ID))
			_ = d.SetKey(starlark.String("seq"), starlark.MakeInt64(m.Seq))
			_ = d.SetKey(starlark.String("role"), starlark.String(string(m.Role)))
			_ = d.SetKey(starlark.String("thread_id"), starlark.String(m.ThreadID))
			_ = d.SetKey(starlark.String("message_id"), starlark.String(m.ID))
			_ = d.SetKey(starlark.String("created_at"), starlark.MakeInt64(m.CreatedAt.UnixMilli()))
			_ = d.SetKey(starlark.String("updated_at"), starlark.MakeInt64(m.UpdatedAt.UnixMilli()))
			_ = d.SetKey(starlark.String("ui_hidden"), starlark.Bool(m.UIHidden))
			if m.ToolCallID != nil {
				_ = d.SetKey(starlark.String("tool_call_id"), starlark.String(strings.TrimSpace(*m.ToolCallID)))
			}
			if m.Name != nil {
				_ = d.SetKey(starlark.String("name"), starlark.String(strings.TrimSpace(*m.Name)))
			}
			if m.Content != nil {
				c := *m.Content
				if maxChars > 0 && len(c) > maxChars {
					c = c[:maxChars]
				}
				_ = d.SetKey(starlark.String("content"), starlark.String(c))
			}
			out = append(out, d)
		}
		messageList := starlark.NewList(out)
		if !withPageInfo && pageToken == "" {
			return messageList, nil
		}
		page := starlark.NewDict(6)
		_ = page.SetKey(starlark.String("messages"), messageList)
		_ = page.SetKey(starlark.String("count"), starlark.MakeInt(len(out)))
		_ = page.SetKey(starlark.String("page_size"), starlark.MakeInt(limit))
		_ = page.SetKey(starlark.String("order_by"), starlark.String(orderBy))
		_ = page.SetKey(starlark.String("has_more"), starlark.Bool(hasMore))
		if hasMore && len(msgs) > 0 {
			nextToken, tokenErr := messagepaging.Encode(messagepaging.Advance(cursor, msgs[len(msgs)-1].Seq))
			if tokenErr != nil {
				return nil, fmt.Errorf("create next message page token: %w", tokenErr)
			}
			_ = page.SetKey(starlark.String("next_page_token"), starlark.String(nextToken))
		}
		return page, nil
	}
}

func toStringSlice(fieldName string, listVal *starlark.List) ([]string, error) {
	if listVal == nil {
		return nil, nil
	}
	out := make([]string, 0, listVal.Len())
	iter := listVal.Iterate()
	defer iter.Done()
	var v starlark.Value
	for iter.Next(&v) {
		sVal, ok := v.(starlark.String)
		if !ok {
			return nil, fmt.Errorf("%s entries must be strings, got %s", fieldName, v.Type())
		}
		s := strings.TrimSpace(string(sVal))
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) > 100 {
		return nil, fmt.Errorf("%s length must be <= 100", fieldName)
	}
	return dedupeRunStrings(out), nil
}

func asInt64(v starlark.Value) (*int64, error) {
	i, ok := v.(starlark.Int)
	if !ok {
		return nil, errors.New("value is not an int")
	}
	i64, ok := i.Int64()
	if !ok {
		return nil, errors.New("integer out of range")
	}
	return &i64, nil
}

func parseRFC3339Timestamp(raw string) (*time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, s); err == nil {
		return &parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &parsed, nil
	}
	return nil, errors.New("timestamp must be RFC3339")
}

func normalizeSearchDocsOrderBy(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "score_desc", "updated_at_desc", "updated_at_asc":
		if strings.TrimSpace(raw) == "" {
			return "score_desc"
		}
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func normalizeListMessagesOrderBy(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "seq_desc", "seq_asc":
		if strings.TrimSpace(raw) == "" {
			return "seq_desc"
		}
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func parseRFC3339TimestampIfAny(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return parseRFC3339Timestamp(raw)
}

func (r *Runtime) consumeEvidenceBudget(s *session, operation string) error {
	if s == nil {
		return errors.New("invalid session")
	}
	if s.evidenceBudget <= 0 {
		return nil
	}
	if s.evidenceUsed >= s.evidenceBudget {
		s.evidenceBudgetExhausted = true
		return fmt.Errorf("evidence budget exhausted for %s", operation)
	}
	s.evidenceUsed++
	if s.evidenceUsed >= s.evidenceBudget {
		s.evidenceBudgetExhausted = true
	}
	return nil
}

func dedupeRunStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func (r *Runtime) addEvidenceBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		id := ""
		note := ""
		if err := starlark.UnpackArgs("add_evidence", args, nil, "id", &id, "note?", &note); err != nil {
			return nil, err
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("id is required")
		}
		if idx, ok := s.evSeen[id]; ok {
			s.evidence[idx].Note = strings.TrimSpace(note)
		} else {
			s.evSeen[id] = len(s.evidence)
			s.evidence = append(s.evidence, Evidence{ID: id, Note: strings.TrimSpace(note)})
		}
		return starlark.None, nil
	}
}

func (r *Runtime) setFinalBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		text := ""
		if err := starlark.UnpackArgs("set_final", args, nil, "text", &text); err != nil {
			return nil, err
		}
		s.final = strings.TrimSpace(text)
		return starlark.None, nil
	}
}

func (r *Runtime) finishBuiltin(s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		text := ""
		var evidence starlark.Value = starlark.None
		if err := starlark.UnpackArgs("finish", args, nil, "text?", &text, "evidence?", &evidence); err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) != "" {
			s.final = strings.TrimSpace(text)
		}
		if evidence != starlark.None {
			if lst, ok := evidence.(*starlark.List); ok {
				iter := lst.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					id := ""
					if sVal, ok := v.(starlark.String); ok {
						id = sVal.GoString()
					} else {
						id = v.String()
					}
					id = strings.TrimSpace(id)
					if id == "" {
						continue
					}
					if _, exists := s.evSeen[id]; exists {
						continue
					}
					s.evSeen[id] = len(s.evidence)
					s.evidence = append(s.evidence, Evidence{ID: id})
				}
			}
		}
		s.done = true
		return starlark.None, nil
	}
}

func (r *Runtime) subLLMBuiltin(ctx context.Context, s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		text := ""
		instruction := ""
		modelOverride := ""
		if err := starlark.UnpackArgs("sub_llm", args, nil, "text", &text, "instruction", &instruction, "model_override?", &modelOverride); err != nil {
			return nil, err
		}
		if s.subCalls >= r.maxSubCalls {
			return nil, errors.New("sub_llm budget reached")
		}
		s.subCalls++
		if r.subLLM != nil {
			summary, err := r.subLLM(ctx, text, instruction, modelOverride)
			if err != nil {
				return nil, err
			}
			return starlark.String(summarizeText(summary, 1200)), nil
		}
		summary := summarizeText(text, 1200)
		if strings.TrimSpace(instruction) != "" {
			summary = strings.TrimSpace(instruction) + ": " + summary
		}
		return starlark.String(summary), nil
	}
}

func (r *Runtime) subRLMBuiltin(ctx context.Context, s *session) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		text := ""
		instruction := ""
		depthBudget := 1
		if err := starlark.UnpackArgs("sub_rlm", args, nil, "text", &text, "instruction", &instruction, "depth_budget?", &depthBudget); err != nil {
			return nil, err
		}
		if depthBudget <= 0 {
			return starlark.String(""), nil
		}
		if s.curDepth >= r.maxRecursionDepth {
			return nil, errors.New("sub_rlm recursion depth exceeded")
		}
		s.curDepth++
		defer func() { s.curDepth-- }()
		if s.subCalls >= r.maxSubCalls {
			return nil, errors.New("sub_rlm budget reached")
		}
		s.subCalls++
		summary := summarizeText(text, 1000)
		if r.subLLM != nil {
			next := summary
			steps := depthBudget
			if steps > r.maxRecursionDepth {
				steps = r.maxRecursionDepth
			}
			for i := 0; i < steps; i++ {
				if s.subCalls >= r.maxSubCalls {
					return nil, errors.New("sub_rlm budget reached")
				}
				s.subCalls++
				prompt := strings.TrimSpace(instruction)
				if prompt == "" {
					prompt = "Summarize and compress this text for retrieval."
				}
				out, err := r.subLLM(ctx, next, prompt, "")
				if err != nil {
					return nil, err
				}
				next = summarizeText(out, 1000)
			}
			summary = next
		} else if strings.TrimSpace(instruction) != "" {
			summary = strings.TrimSpace(instruction) + ": " + summary
		}
		return starlark.String(summary), nil
	}
}

func summarizeText(text string, max int) string {
	raw := strings.TrimSpace(text)
	if max <= 0 {
		return ""
	}
	if len(raw) <= max {
		return raw
	}
	return raw[:max]
}

func readArtifactFile(cfgDir string, rel string, maxBytes int64) (string, error) {
	clean, err := cleanRelPath(rel)
	if err != nil {
		return "", err
	}
	full := filepath.Join(cfgDir, filepath.FromSlash(clean))
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > maxBytes {
		b = b[:maxBytes]
	}
	return string(b), nil
}

func cleanRelPath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.TrimPrefix(raw, "/")
	clean := path.Clean(raw)
	if clean == "." || clean == "" {
		return "", errors.New("invalid path")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path traversal is not allowed")
	}
	if strings.ContainsRune(clean, '\x00') {
		return "", errors.New("invalid path")
	}
	return clean, nil
}
