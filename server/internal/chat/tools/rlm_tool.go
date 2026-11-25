package tools

import (
	"context"
	"encoding/json"
	"strings"

	"ai-analyst-local/internal/chat/rlm"
	"ai-analyst-local/internal/chat/types"
)

func rlmResultMetadata(kind string, extra map[string]any) map[string]any {
	meta := map[string]any{
		"provider":  "thread-search",
		"product":   "rlm",
		"family":    "thread-search",
		"kind":      kind,
		"tool_name": "rlm_exec",
		"readonly":  false,
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		meta[key] = value
	}
	return toolCatalogResultMetadata("rlm_exec", kind, meta)
}

// sanitizeRLMQueryRetryResult closes the indirect retrieval path through the
// Starlark runtime. JSON round-tripping converts Evidence structs, pointers,
// and nested values into the same generic shape used by retrieval sanitizers.
// RLM output is not authoritative provenance, so no qob is portable through
// it; ordinary job/polling handles and business fields remain intact.
func sanitizeRLMQueryRetryResult(value map[string]any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var generic map[string]any
	if json.Unmarshal(encoded, &generic) != nil {
		return value
	}
	clean, _ := sanitizeRLMQueryRetryValue(generic).(map[string]any)
	if clean == nil {
		return map[string]any{}
	}
	return clean
}

func sanitizeRLMQueryRetryValue(value any) any {
	return sanitizeRLMQueryRetryValueInContext(value, false)
}

func sanitizeRLMQueryRetryValueInContext(value any, inheritedLifecycleContext bool) any {
	switch typed := value.(type) {
	case map[string]any:
		stripLegacyTargets := inheritedLifecycleContext
		for key, nested := range typed {
			normalized := normalizeRetrievedQueryRetryKey(key)
			switch normalized {
			case "querymetadata", "retrymetadata", "queryobligationid", "queryobligationmanaged", "replacementwaitrequired",
				"replacementpreflightfailed", "replacementpreflightretryrequired", "monitorrecoverypending":
				stripLegacyTargets = true
			}
			if normalized == "toolname" && isRetrievedQueryLifecycleToolName(strings.TrimSpace(toString(nested))) {
				stripLegacyTargets = true
			}
		}
		clean := make(map[string]any, len(typed))
		for key, nested := range typed {
			if sanitizedKey := sanitizeRetrievedQueryRetryText(key, false, false); sanitizedKey != key {
				continue
			}
			normalized := normalizeRetrievedQueryRetryKey(key)
			if normalized == "queryobligationid" || (stripLegacyTargets && isRetrievedQueryRetryIdentityKey(normalized)) {
				continue
			}
			childLifecycleContext := false
			switch normalized {
			case "querymetadata", "retrymetadata":
				childLifecycleContext = true
			}
			clean[key] = sanitizeRLMQueryRetryValueInContext(nested, childLifecycleContext)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index, nested := range typed {
			clean[index] = sanitizeRLMQueryRetryValueInContext(nested, inheritedLifecycleContext)
		}
		return clean
	case string:
		stripLegacyTargets := retrievedTextHasQueryLifecycleEnvelope(typed) || retrievedSearchDocHasQueryLifecycle(typed)
		return sanitizeRetrievedQueryRetryText(typed, false, stripLegacyTargets)
	default:
		return value
	}
}

func toString(value any) string {
	text, _ := value.(string)
	return text
}

func newRLMExecTool(runtime *rlm.Runtime) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "rlm_exec",
				Description: "Execute Starlark code in a bounded retrieval workspace session and return compact execution state. Use only supported Starlark helpers (search, get_doc, get_artifact, list_messages, prompt_len, prompt_peek, prompt_slice, set_prompt, append_prompt, clear_prompt, add_evidence, set_final, finish, sub_llm, sub_rlm). For message pagination call list_messages(thread_id=..., with_page_info=True), then copy next_page_token into page_token. open(), read_file(), f-strings, and search(..., max_results=...) are not supported.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code": map[string]any{
							"type":        "string",
							"description": "Starlark code to execute. Do not use Python-style calls like open(), read_file(), f-strings, dir() with no argument, or search(..., max_results=).",
						},
						"note": map[string]any{
							"type":        "string",
							"description": "Optional short operation note.",
						},
						"dry_run": map[string]any{
							"type":        "boolean",
							"description": "Validate and parse the Starlark code without executing it or mutating the retrieval workspace.",
						},
					},
					"required":             []string{"code"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				Code   string `json:"code"`
				Note   string `json:"note,omitempty"`
				DryRun bool   `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			code := strings.TrimSpace(in.Code)
			if err := rlm.ValidateCode(code); err != nil {
				return JSONError(err.Error()), nil
			}
			if in.DryRun {
				return JSONOKWithMetadata(map[string]any{
					"ok":         true,
					"dry_run":    true,
					"validated":  true,
					"code_bytes": len(code),
					"message":    "Dry run only. RLM code was validated and not executed.",
				}, rlmResultMetadata("dry_run", map[string]any{
					"mode":       "dry_run",
					"validated":  true,
					"code_bytes": len(code),
				})), nil
			}
			if runtime == nil {
				return JSONError("rlm runtime is not configured"), nil
			}
			runID := strings.TrimSpace(exec.RunID)
			if runID == "" {
				return JSONError("missing run context"), nil
			}
			res, err := runtime.Exec(ctx, runID, rlm.ExecRequest{
				Code:           code,
				Note:           in.Note,
				Depth:          exec.SearchDepth,
				EvidenceBudget: exec.EvidenceBudget,
			})
			if err != nil {
				return JSONError(err.Error()), nil
			}
			qm := map[string]any{}
			if res.EvidenceBudget > 0 {
				qm["evidence_budget"] = res.EvidenceBudget
			}
			if res.EvidenceUsed > 0 {
				qm["evidence_used"] = res.EvidenceUsed
			}
			if res.EvidenceBudgetExhausted {
				qm["evidence_budget_exhausted"] = true
			}
			resultPreview := res.Preview()
			result := sanitizeRLMQueryRetryResult(map[string]any{
				"ok":               res.OK,
				"stdout_prefix":    res.StdoutPrefix,
				"stdout_len":       res.StdoutLen,
				"prompt_len":       res.PromptLen,
				"evidence":         res.Evidence,
				"done":             res.Done,
				"result_preview":   resultPreview,
				"final_answer":     res.FinalAnswer,
				"last_ops_summary": res.LastOpsSummary,
				"error":            res.Error,
			})
			return Output{
				OK:            true,
				ResultFormat:  ResultFormatJSON,
				Result:        result,
				QueryMetadata: qm,
				ResultMetadata: rlmResultMetadata("execution", map[string]any{
					"ok":                        res.OK,
					"done":                      res.Done,
					"stdout_len":                res.StdoutLen,
					"prompt_len":                res.PromptLen,
					"evidence_count":            len(res.Evidence),
					"evidence_budget":           res.EvidenceBudget,
					"evidence_used":             res.EvidenceUsed,
					"evidence_budget_exhausted": res.EvidenceBudgetExhausted,
				}),
			}, nil
		},
	}
}
