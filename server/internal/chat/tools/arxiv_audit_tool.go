package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	arxivapi "ai-analyst-local/internal/arxiv"
	"ai-analyst-local/internal/chat/store"
	chattypes "ai-analyst-local/internal/chat/types"
)

const (
	arxivAuditSchema           = "arxiv_research_audit.v1"
	arxivRecommendationsSchema = "arxiv_recommendations.v1"
	arxivAuditMessagePageSize  = 500
	arxivAuditMaxMessages      = 5000
	arxivAuditMaxPaperText     = 1_000_000
	arxivAuditTextWindowChars  = 200_000
)

type arxivScreeningReportArgs struct {
	InterpretedRequest string                     `json:"interpreted_request,omitempty"`
	Concepts           []string                   `json:"concepts,omitempty"`
	CorpusGaps         []string                   `json:"corpus_gaps,omitempty"`
	QueryRationales    []arxivQueryRationaleInput `json:"query_rationales,omitempty"`
	Verdicts           []arxivVerdictInput        `json:"verdicts"`
}

type arxivQueryRationaleInput struct {
	ToolCallID          string   `json:"tool_call_id"`
	Phase               string   `json:"phase,omitempty"`
	Rationale           string   `json:"rationale"`
	DerivedFromArxivIDs []string `json:"derived_from_arxiv_ids,omitempty"`
}

type arxivVerdictInput struct {
	ArxivID  string               `json:"arxiv_id"`
	Version  int                  `json:"version,omitempty"`
	Status   string               `json:"status"`
	Reason   string               `json:"reason,omitempty"`
	Evidence []arxivEvidenceInput `json:"evidence,omitempty"`
}

type arxivEvidenceInput struct {
	SourceField     string `json:"source_field"`
	Quote           string `json:"quote"`
	OccurrenceIndex *int   `json:"occurrence_index,omitempty"`
}

type arxivRecommendationsArgs struct {
	ScreeningReportToolCallID string                     `json:"screening_report_tool_call_id,omitempty"`
	SelectionBasis            string                     `json:"selection_basis,omitempty"`
	Recommendations           []arxivRecommendationInput `json:"recommendations"`
}

type arxivRecommendationInput struct {
	ArxivID      string                             `json:"arxiv_id"`
	Rank         int                                `json:"rank,omitempty"`
	Tier         string                             `json:"tier,omitempty"`
	Reason       string                             `json:"reason,omitempty"`
	UseFor       string                             `json:"use_for,omitempty"`
	Caveats      []string                           `json:"caveats,omitempty"`
	EvidenceRefs []arxivRecommendationEvidenceInput `json:"evidence_refs,omitempty"`
}

type arxivRecommendationEvidenceInput struct {
	EvidenceIndex int    `json:"evidence_index"`
	SourceField   string `json:"source_field,omitempty"`
}

type arxivAuditWarning struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	ArxivID     string `json:"arxiv_id,omitempty"`
	ToolCallID  string `json:"tool_call_id,omitempty"`
	SourceField string `json:"source_field,omitempty"`
}

type arxivAuditQueryTrace struct {
	ToolCallID      string
	Seq             int64
	Args            map[string]any
	TotalResults    int
	Start           int
	ItemsPerPage    int
	ReturnedIDs     []string
	ReturnedEntries []arxivAuditPaper
}

type arxivAuditReadTrace struct {
	ToolCallID     string
	Seq            int64
	ArxivID        string
	PaperKey       string
	Title          string
	PageCount      int
	StartPage      int
	EndPage        int
	PagesRead      int
	ExtractState   string
	ExtractQuality string
	SourceKind     string
}

type arxivAuditPaper struct {
	ArxivID         string
	Version         int
	Title           string
	Summary         string
	Authors         []string
	Categories      []string
	PrimaryCategory string
	Published       string
	Updated         string
	AbsURL          string
	PDFURL          string
	Downloaded      bool
	PaperKey        string
	FoundBy         []string
	DuplicateIn     []string
	FirstSeen       string
	FirstSeenIndex  int
}

type arxivAuditContext struct {
	ThreadID string
	RunID    string
	Queries  []arxivAuditQueryTrace
	Papers   map[string]*arxivAuditPaper
	Reads    map[string][]arxivAuditReadTrace
	Warnings []arxivAuditWarning
}

type arxivScreeningReportTrace struct {
	ToolCallID string
	ArtifactID string
	Seq        int64
	ThreadID   string
	RunID      string
	Report     map[string]any
	Papers     map[string]arxivScreeningPaperRef
}

type arxivScreeningPaperRef struct {
	ArxivID      string
	Version      int
	Status       string
	Index        int
	Evidence     []map[string]any
	ReadState    string
	ReadCoverage map[string]any
}

func newArxivScreeningReportTool(manager *arxivapi.Manager) Tool {
	return ExecutableTool{
		Def: arxivToolDefinition(
			ArxivToolScreeningReport,
			"Validate and persist an audit report for the current run's arXiv searches. Use after arxiv_search calls to record agent interpretation, query rationales, screening verdicts, and exact evidence quotes. This tool does not search arXiv; it validates against already-returned current-run arXiv results.",
			objectSchema(map[string]any{
				"interpreted_request": stringSchema("One concise sentence describing what the agent understood the research request to mean."),
				"concepts": map[string]any{
					"type":        "array",
					"description": "Short concept chips the agent used to guide search strategy.",
					"items":       map[string]any{"type": "string"},
				},
				"corpus_gaps": map[string]any{
					"type":        "array",
					"description": "Corpus limitations or likely gaps visible from arXiv-only search.",
					"items":       map[string]any{"type": "string"},
				},
				"query_rationales": map[string]any{
					"type":        "array",
					"description": "One rationale per arXiv search tool call.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"tool_call_id":           stringSchema("Tool call ID of a prior arxiv_search in this run."),
							"phase":                  stringSchema("Optional strategy phase such as sweep, refine, exact_lookup, or stopping_check."),
							"rationale":              stringSchema("Why this query was run."),
							"derived_from_arxiv_ids": stringArraySchema("Prior arXiv IDs that seeded this query, if any."),
						},
						"required": []string{"tool_call_id", "rationale"},
					},
				},
				"verdicts": map[string]any{
					"type":        "array",
					"description": "Agent screening verdicts for papers returned earlier in this run.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"arxiv_id": stringSchema("arXiv ID of a paper returned by prior arxiv_search in this run."),
							"version":  intSchema("Optional arXiv version number when known."),
							"status": map[string]any{
								"type":        "string",
								"description": "Screening status: kept, possible, or screened_out.",
								"enum":        []string{"kept", "possible", "screened_out"},
							},
							"reason": stringSchema("One concise agent-authored reason for the verdict."),
							"evidence": map[string]any{
								"type":        "array",
								"description": "Exact quotes from title, summary, or locally stored paper_text that support the verdict.",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"source_field": map[string]any{
											"type":        "string",
											"description": "Source field containing the exact quote.",
											"enum":        []string{"title", "summary", "paper_text"},
										},
										"quote":            stringSchema("Exact substring to validate. Do not paraphrase."),
										"occurrence_index": intSchema("Zero-based occurrence to use when the exact quote appears multiple times."),
									},
									"required": []string{"source_field", "quote"},
								},
							},
						},
						"required": []string{"arxiv_id", "status"},
					},
				},
			}, []string{"verdicts"}),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in arxivScreeningReportArgs
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			audit, err := buildArxivScreeningReport(ctx, manager, exec, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return JSONOKWithMetadata(audit, arxivScreeningReportResultMetadata()), nil
		},
	}
}

func newArxivRecommendationsTool(_ *arxivapi.Manager) Tool {
	return ExecutableTool{
		Def: arxivToolDefinition(
			ArxivToolRecommendations,
			"Validate and persist a compact arXiv recommendation set linked to a prior arxiv_screening_report in this run. Use this instead of emitting a markdown recommendation table; reference arXiv IDs and screening report evidence rather than duplicating paper metadata.",
			objectSchema(map[string]any{
				"screening_report_tool_call_id": stringSchema("Optional tool_call_id of the arxiv_screening_report to use. Defaults to the latest screening report in this run."),
				"selection_basis":               stringSchema("One concise sentence describing how the recommended set was selected from the screening report."),
				"recommendations": map[string]any{
					"type":        "array",
					"description": "Compact recommendation rows. Do not copy title, abstract, authors, or other full paper metadata from the screening report.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"arxiv_id": stringSchema("arXiv ID of a kept or possible paper in the linked arxiv_screening_report."),
							"rank":     map[string]any{"type": "integer", "minimum": 1, "description": "Optional 1-based rank. Defaults to input order."},
							"tier": map[string]any{
								"type":        "string",
								"description": "Recommendation tier.",
								"enum":        []string{"primary", "secondary", "background"},
							},
							"reason":  stringSchema("Concise recommendation rationale authored by the agent."),
							"use_for": stringSchema("Optional concise note about what this paper is useful for."),
							"caveats": stringArraySchema("Optional concise caveats for this recommendation."),
							"evidence_refs": map[string]any{
								"type":        "array",
								"description": "References to validated evidence entries already present on the paper in the linked screening report.",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"evidence_index": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based index into the paper's evidence array in the screening report."},
										"source_field":   stringSchema("Optional expected evidence source field such as title, summary, or paper_text."),
									},
									"required": []string{"evidence_index"},
								},
							},
						},
						"required": []string{"arxiv_id"},
					},
				},
			}, []string{"recommendations"}),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in arxivRecommendationsArgs
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			recs, err := buildArxivRecommendations(ctx, exec, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			resultMetadata := arxivRecommendationsResultMetadata(recs)
			artifactContent, _ := json.MarshalIndent(recs, "", "  ")
			lang := "json"
			out := JSONOKWithMetadata(recs, resultMetadata)
			out.Artifact = &ArtifactPayload{
				Type:     "code",
				Title:    "arXiv Recommendations",
				Language: &lang,
				Content:  string(artifactContent),
				Metadata: arxivRecommendationsArtifactMetadata(recs, in, resultMetadata),
			}
			return out, nil
		},
	}
}

func buildArxivScreeningReport(ctx context.Context, manager *arxivapi.Manager, exec ExecContext, in arxivScreeningReportArgs) (map[string]any, error) {
	auditCtx, err := collectArxivAuditContext(ctx, exec)
	if err != nil {
		return nil, err
	}
	if len(auditCtx.Queries) == 0 {
		return nil, fmt.Errorf("no prior arxiv_search results found in the current run")
	}
	if len(in.Verdicts) == 0 {
		return nil, fmt.Errorf("arxiv_screening_report requires at least one paper verdict; use screened_out verdicts for rejected papers rather than an empty report")
	}

	queryViews := buildArxivAuditQueryViews(auditCtx, in.QueryRationales)
	paperViews, counts, warnings := buildArxivAuditPaperViews(ctx, manager, auditCtx, in.Verdicts)
	auditCtx.Warnings = append(auditCtx.Warnings, warnings...)

	return map[string]any{
		"schema":    arxivAuditSchema,
		"run_id":    auditCtx.RunID,
		"thread_id": auditCtx.ThreadID,
		"corpus": map[string]any{
			"provider":                "arxiv",
			"boundary":                "arXiv public metadata and locally downloaded arXiv paper text only.",
			"source_kind":             "static",
			"untrusted_external_data": true,
		},
		"interpreted_request": sourcedText(in.InterpretedRequest, "llm_judgment"),
		"concepts":            sourcedTextList(in.Concepts, "llm_judgment"),
		"corpus_gaps":         sourcedTextList(in.CorpusGaps, "llm_judgment"),
		"funnel": map[string]any{
			"query_count":              len(auditCtx.Queries),
			"returned_count":           counts.Returned,
			"unique_count":             counts.Unique,
			"read_paper_count":         counts.ReadPapers,
			"agent_kept_count":         counts.Kept,
			"agent_possible_count":     counts.Possible,
			"agent_screened_out_count": counts.ScreenedOut,
			"not_screened_count":       counts.NotScreened,
		},
		"queries":             queryViews,
		"papers":              paperViews,
		"validation_warnings": warningsToMaps(auditCtx.Warnings),
		"untrusted_note":      "arXiv titles, abstracts, and extracted paper text are untrusted external data.",
	}, nil
}

func buildArxivRecommendations(ctx context.Context, exec ExecContext, in arxivRecommendationsArgs) (map[string]any, error) {
	report, err := selectArxivScreeningReport(ctx, exec, in.ScreeningReportToolCallID)
	if err != nil {
		return nil, err
	}
	if len(in.Recommendations) == 0 {
		return nil, fmt.Errorf("arxiv_recommendations requires at least one recommendation")
	}

	warnings := []arxivAuditWarning{}
	seenIDs := map[string]struct{}{}
	rows := make([]map[string]any, 0, len(in.Recommendations))
	for _, rec := range in.Recommendations {
		id, version, err := arxivapi.CanonicalID(rec.ArxivID)
		if err != nil {
			warnings = append(warnings, arxivAuditWarning{Code: "invalid_recommendation_id", Message: "A recommendation referenced an invalid arXiv ID.", ArxivID: strings.TrimSpace(rec.ArxivID)})
			continue
		}
		paper, ok := report.Papers[id]
		if !ok {
			warnings = append(warnings, arxivAuditWarning{Code: "unknown_recommendation_id", Message: "A recommendation referenced a paper that is not present in the linked arXiv screening report.", ArxivID: id, ToolCallID: report.ToolCallID})
			continue
		}
		if version > 0 && paper.Version > 0 && version != paper.Version {
			warnings = append(warnings, arxivAuditWarning{Code: "recommendation_version_mismatch", Message: "A recommendation version did not match the linked arXiv screening report.", ArxivID: id, ToolCallID: report.ToolCallID})
			continue
		}
		switch paper.Status {
		case "kept", "possible":
		default:
			warnings = append(warnings, arxivAuditWarning{Code: "not_recommendable_status", Message: "Recommendations must reference papers marked kept or possible in the linked screening report.", ArxivID: id, ToolCallID: report.ToolCallID})
			continue
		}
		if _, exists := seenIDs[id]; exists {
			warnings = append(warnings, arxivAuditWarning{Code: "duplicate_recommendation", Message: "Multiple recommendations referenced the same arXiv ID; the first valid recommendation was used.", ArxivID: id, ToolCallID: report.ToolCallID})
			continue
		}
		seenIDs[id] = struct{}{}

		rank := rec.Rank
		if rank <= 0 {
			rank = len(rows) + 1
		}
		tier := normalizeArxivRecommendationTier(rec.Tier)
		if strings.TrimSpace(rec.Tier) != "" && tier == "" {
			warnings = append(warnings, arxivAuditWarning{Code: "invalid_recommendation_tier", Message: "Recommendation tier must be primary, secondary, or background.", ArxivID: id, ToolCallID: report.ToolCallID})
		}
		evidenceRefs, refWarnings := validateArxivRecommendationEvidenceRefs(report, paper, rec.EvidenceRefs)
		warnings = append(warnings, refWarnings...)
		if len(paper.ReadCoverage) == 0 || paper.ReadState != "read" {
			warnings = append(warnings, arxivAuditWarning{Code: "recommendation_without_read_trace", Message: "A recommended paper has no deterministic arxiv_read_paper trace in the linked screening report.", ArxivID: id, ToolCallID: report.ToolCallID})
		}

		row := map[string]any{
			"rank":                          rank,
			"arxiv_id":                      id,
			"screening_status":              paper.Status,
			"screening_report_tool_call_id": report.ToolCallID,
			"screening_report_paper_index":  paper.Index,
			"source_kind":                   "validated",
		}
		if paper.Version > 0 {
			row["version"] = paper.Version
		}
		if report.ArtifactID != "" {
			row["screening_report_artifact_id"] = report.ArtifactID
		}
		if paper.ReadState != "" {
			row["read_state"] = paper.ReadState
		}
		if len(paper.ReadCoverage) > 0 {
			row["read_coverage"] = cloneAnyMap(paper.ReadCoverage)
		}
		if tier != "" {
			row["tier"] = tier
		}
		if reason := sourcedText(rec.Reason, "llm_judgment"); reason != nil {
			row["reason"] = reason
		}
		if useFor := sourcedText(rec.UseFor, "llm_judgment"); useFor != nil {
			row["use_for"] = useFor
		}
		if caveats := sourcedTextList(rec.Caveats, "llm_judgment"); len(caveats) > 0 {
			row["caveats"] = caveats
		}
		if len(evidenceRefs) > 0 {
			row["evidence_refs"] = evidenceRefs
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("arxiv_recommendations did not contain any valid kept or possible papers from the linked screening report")
	}

	return map[string]any{
		"schema":    arxivRecommendationsSchema,
		"run_id":    report.RunID,
		"thread_id": report.ThreadID,
		"screening_report": map[string]any{
			"schema":       arxivAuditSchema,
			"tool_call_id": report.ToolCallID,
			"artifact_id":  report.ArtifactID,
		},
		"selection_basis":      sourcedText(in.SelectionBasis, "llm_judgment"),
		"metadata_resolution":  "Resolve title, authors, abstract, and evidence text from the linked arxiv_screening_report.",
		"recommendation_count": len(rows),
		"recommendations":      rows,
		"validation_warnings":  warningsToMaps(warnings),
		"untrusted_note":       "Recommended arXiv papers are linked to an arXiv screening report built from untrusted external paper metadata and extracted text.",
	}, nil
}

type arxivAuditCounts struct {
	Returned    int
	Unique      int
	Kept        int
	Possible    int
	ScreenedOut int
	NotScreened int
	ReadPapers  int
}

func collectArxivAuditContext(ctx context.Context, exec ExecContext) (*arxivAuditContext, error) {
	if exec.Store == nil {
		return nil, fmt.Errorf("store is not configured")
	}
	threadID := strings.TrimSpace(exec.ThreadID)
	runID := strings.TrimSpace(exec.RunID)
	if threadID == "" || runID == "" {
		return nil, fmt.Errorf("thread_id and run_id are required to build an arXiv screening report")
	}

	argsByCallID := map[string]map[string]any{}
	auditCtx := &arxivAuditContext{
		ThreadID: threadID,
		RunID:    runID,
		Papers:   map[string]*arxivAuditPaper{},
		Reads:    map[string][]arxivAuditReadTrace{},
	}
	afterSeq := int64(0)
	seenMessages := 0
	for {
		msgs, err := exec.Store.ListMessages(ctx, store.ListMessagesParams{
			ThreadID:      threadID,
			AfterSeq:      &afterSeq,
			Limit:         arxivAuditMessagePageSize,
			IncludeHidden: true,
			OrderBy:       "seq_asc",
		})
		if err != nil {
			return nil, err
		}
		if len(msgs) == 0 {
			break
		}
		for _, msg := range msgs {
			if msg.Seq > afterSeq {
				afterSeq = msg.Seq
			}
			if msg.RunID == nil || strings.TrimSpace(*msg.RunID) != runID {
				continue
			}
			seenMessages++
			if seenMessages > arxivAuditMaxMessages {
				return nil, fmt.Errorf("current run exceeded the arXiv audit collector budget")
			}
			for _, call := range msg.ToolCalls {
				if strings.TrimSpace(call.Name) != ArxivToolSearch || strings.TrimSpace(call.ID) == "" {
					continue
				}
				argsByCallID[strings.TrimSpace(call.ID)] = cloneAnyMap(call.Args)
			}
			if msg.Role != chattypes.RoleTool || msg.Content == nil {
				continue
			}
			if trace, ok := arxivAuditTraceFromMessage(ctx, exec, msg, argsByCallID); ok {
				auditCtx.Queries = append(auditCtx.Queries, trace)
				queryIndex := len(auditCtx.Queries) - 1
				for _, paper := range trace.ReturnedEntries {
					p := paper
					existing := auditCtx.Papers[p.ArxivID]
					if existing == nil {
						p.FirstSeen = trace.ToolCallID
						p.FirstSeenIndex = queryIndex
						p.FoundBy = []string{trace.ToolCallID}
						auditCtx.Papers[p.ArxivID] = &p
						continue
					}
					existing.FoundBy = appendUniqueString(existing.FoundBy, trace.ToolCallID)
					existing.DuplicateIn = appendUniqueString(existing.DuplicateIn, trace.ToolCallID)
					if existing.PaperKey == "" {
						existing.PaperKey = p.PaperKey
					}
					existing.Downloaded = existing.Downloaded || p.Downloaded
				}
				continue
			}
			if readTrace, ok := arxivReadTraceFromMessage(msg); ok {
				auditCtx.Reads[readTrace.ArxivID] = append(auditCtx.Reads[readTrace.ArxivID], readTrace)
			}
		}
		if len(msgs) < arxivAuditMessagePageSize {
			break
		}
	}
	return auditCtx, nil
}

func arxivAuditTraceFromMessage(ctx context.Context, exec ExecContext, msg store.Message, argsByCallID map[string]map[string]any) (arxivAuditQueryTrace, bool) {
	env, ok := chattypes.ParseToolResultEnvelope(derefString(msg.Content))
	if !ok || !env.OK || strings.TrimSpace(env.ToolName) != ArxivToolSearch {
		return arxivAuditQueryTrace{}, false
	}
	if kind := strings.TrimSpace(arxivAuditStringFromMap(env.ResultMetadata, "kind")); kind != "search_results" {
		return arxivAuditQueryTrace{}, false
	}
	callID := strings.TrimSpace(env.ToolCallID)
	result, ok := arxivSearchResultMapFromEnvelope(ctx, exec, env)
	if !ok {
		return arxivAuditQueryTrace{}, false
	}
	trace := arxivAuditQueryTrace{
		ToolCallID:   callID,
		Seq:          msg.Seq,
		Args:         cloneAnyMap(argsByCallID[callID]),
		TotalResults: arxivAuditIntFromAny(result["total_results"]),
		Start:        arxivAuditIntFromAny(result["start"]),
		ItemsPerPage: arxivAuditIntFromAny(result["items_per_page"]),
	}
	for _, rawEntry := range arxivAuditEntryMaps(result["entries"]) {
		entryMap := rawEntry
		id, version, err := arxivapi.CanonicalID(arxivAuditStringFromMap(entryMap, "arxiv_id"))
		if err != nil {
			continue
		}
		if version == 0 {
			version = arxivAuditIntFromAny(entryMap["version"])
		}
		paper := arxivAuditPaper{
			ArxivID:         id,
			Version:         version,
			Title:           boundAuditText(arxivAuditStringFromMap(entryMap, "title"), 1000),
			Summary:         boundAuditText(arxivAuditStringFromMap(entryMap, "summary"), 5000),
			Authors:         arxivAuditStringsFromAny(entryMap["authors"]),
			Categories:      arxivAuditStringsFromAny(entryMap["categories"]),
			PrimaryCategory: arxivAuditStringFromMap(entryMap, "primary_category"),
			Published:       arxivAuditStringFromAny(entryMap["published"]),
			Updated:         arxivAuditStringFromAny(entryMap["updated"]),
			AbsURL:          arxivAuditStringFromMap(entryMap, "abs_url"),
			PDFURL:          arxivAuditStringFromMap(entryMap, "pdf_url"),
			Downloaded:      arxivAuditBoolFromAny(entryMap["downloaded"]),
			PaperKey:        arxivAuditStringFromMap(entryMap, "paper_key"),
		}
		trace.ReturnedIDs = append(trace.ReturnedIDs, id)
		trace.ReturnedEntries = append(trace.ReturnedEntries, paper)
	}
	return trace, true
}

func arxivSearchResultMapFromEnvelope(ctx context.Context, exec ExecContext, env chattypes.ToolResultEnvelope) (map[string]any, bool) {
	result, ok := env.Result.(map[string]any)
	if ok && len(arxivAuditEntryMaps(result["entries"])) > 0 {
		return result, true
	}
	if exec.Store == nil || strings.TrimSpace(env.ArtifactID) == "" {
		return result, ok
	}
	artifact, err := exec.Store.GetArtifact(ctx, strings.TrimSpace(env.ArtifactID))
	if err != nil {
		return result, ok
	}
	content, truncated, _, err := readArtifactContentPrefix(exec.CfgDir, artifact, 8<<20)
	if err != nil || truncated {
		return result, ok
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return result, ok
	}
	if nested, nestedOK := raw["result"].(map[string]any); nestedOK && len(arxivAuditEntryMaps(nested["entries"])) > 0 {
		return nested, true
	}
	if len(arxivAuditEntryMaps(raw["entries"])) > 0 {
		return raw, true
	}
	return result, ok
}

func arxivReadTraceFromMessage(msg store.Message) (arxivAuditReadTrace, bool) {
	env, ok := chattypes.ParseToolResultEnvelope(derefString(msg.Content))
	if !ok || !env.OK || strings.TrimSpace(env.ToolName) != ArxivToolReadPaper {
		return arxivAuditReadTrace{}, false
	}
	if kind := strings.TrimSpace(arxivAuditStringFromMap(env.ResultMetadata, "kind")); kind != "paper_pages" {
		return arxivAuditReadTrace{}, false
	}
	result, ok := env.Result.(map[string]any)
	if !ok {
		return arxivAuditReadTrace{}, false
	}
	pageWindow, ok := firstMapFromAny(result["paper_pages"])
	if !ok {
		return arxivAuditReadTrace{}, false
	}
	id, _, err := arxivapi.CanonicalID(arxivAuditStringFromMap(pageWindow, "arxiv_id"))
	if err != nil {
		return arxivAuditReadTrace{}, false
	}
	callID := strings.TrimSpace(env.ToolCallID)
	if callID == "" {
		callID = strings.TrimSpace(derefString(msg.ToolCallID))
	}
	startPage := arxivAuditIntFromAny(pageWindow["start_page"])
	endPage := arxivAuditIntFromAny(pageWindow["end_page"])
	pagesRead := len(arxivAuditEntryMaps(pageWindow["pages"]))
	if pagesRead == 0 && startPage > 0 && endPage >= startPage {
		pagesRead = endPage - startPage + 1
	}
	return arxivAuditReadTrace{
		ToolCallID:     callID,
		Seq:            msg.Seq,
		ArxivID:        id,
		PaperKey:       arxivAuditStringFromMap(pageWindow, "paper_key"),
		Title:          boundAuditText(arxivAuditStringFromMap(pageWindow, "title"), 1000),
		PageCount:      arxivAuditIntFromAny(pageWindow["page_count"]),
		StartPage:      startPage,
		EndPage:        endPage,
		PagesRead:      pagesRead,
		ExtractState:   arxivAuditStringFromMap(pageWindow, "extract_state"),
		ExtractQuality: arxivAuditStringFromMap(pageWindow, "extract_quality"),
		SourceKind:     arxivAuditStringFromMap(pageWindow, "source_kind"),
	}, true
}

func selectArxivScreeningReport(ctx context.Context, exec ExecContext, preferredToolCallID string) (arxivScreeningReportTrace, error) {
	reports, err := collectArxivScreeningReports(ctx, exec)
	if err != nil {
		return arxivScreeningReportTrace{}, err
	}
	if len(reports) == 0 {
		return arxivScreeningReportTrace{}, fmt.Errorf("no prior arxiv_screening_report found in the current run")
	}
	preferredToolCallID = strings.TrimSpace(preferredToolCallID)
	if preferredToolCallID == "" {
		return reports[len(reports)-1], nil
	}
	for _, report := range reports {
		if report.ToolCallID == preferredToolCallID {
			return report, nil
		}
	}
	return arxivScreeningReportTrace{}, fmt.Errorf("arxiv_screening_report tool_call_id %q was not found in the current run", preferredToolCallID)
}

func collectArxivScreeningReports(ctx context.Context, exec ExecContext) ([]arxivScreeningReportTrace, error) {
	if exec.Store == nil {
		return nil, fmt.Errorf("store is not configured")
	}
	threadID := strings.TrimSpace(exec.ThreadID)
	runID := strings.TrimSpace(exec.RunID)
	if threadID == "" || runID == "" {
		return nil, fmt.Errorf("thread_id and run_id are required to build arXiv recommendations")
	}

	reports := []arxivScreeningReportTrace{}
	afterSeq := int64(0)
	seenMessages := 0
	for {
		msgs, err := exec.Store.ListMessages(ctx, store.ListMessagesParams{
			ThreadID:      threadID,
			AfterSeq:      &afterSeq,
			Limit:         arxivAuditMessagePageSize,
			IncludeHidden: true,
			OrderBy:       "seq_asc",
		})
		if err != nil {
			return nil, err
		}
		if len(msgs) == 0 {
			break
		}
		for _, msg := range msgs {
			if msg.Seq > afterSeq {
				afterSeq = msg.Seq
			}
			if msg.RunID == nil || strings.TrimSpace(*msg.RunID) != runID {
				continue
			}
			seenMessages++
			if seenMessages > arxivAuditMaxMessages {
				return nil, fmt.Errorf("current run exceeded the arXiv recommendation collector budget")
			}
			if msg.Role != chattypes.RoleTool || msg.Content == nil {
				continue
			}
			report, ok := arxivScreeningReportFromMessage(ctx, exec, msg)
			if !ok {
				continue
			}
			report.ThreadID = threadID
			report.RunID = runID
			reports = append(reports, report)
		}
		if len(msgs) < arxivAuditMessagePageSize {
			break
		}
	}
	return reports, nil
}

func arxivScreeningReportFromMessage(ctx context.Context, exec ExecContext, msg store.Message) (arxivScreeningReportTrace, bool) {
	env, ok := chattypes.ParseToolResultEnvelope(derefString(msg.Content))
	if !ok || !env.OK || strings.TrimSpace(env.ToolName) != ArxivToolScreeningReport {
		return arxivScreeningReportTrace{}, false
	}
	if kind := strings.TrimSpace(arxivAuditStringFromMap(env.ResultMetadata, "kind")); kind != "" && kind != "screening_report" {
		return arxivScreeningReportTrace{}, false
	}
	reportMap, ok := arxivScreeningReportMapFromEnvelope(ctx, exec, env)
	if !ok {
		return arxivScreeningReportTrace{}, false
	}
	toolCallID := strings.TrimSpace(env.ToolCallID)
	if toolCallID == "" {
		toolCallID = strings.TrimSpace(derefString(msg.ToolCallID))
	}
	trace := arxivScreeningReportTrace{
		ToolCallID: toolCallID,
		ArtifactID: strings.TrimSpace(env.ArtifactID),
		Seq:        msg.Seq,
		Report:     reportMap,
		Papers:     map[string]arxivScreeningPaperRef{},
	}
	for idx, rawPaper := range arxivAuditEntryMaps(reportMap["papers"]) {
		id, version, err := arxivapi.CanonicalID(arxivAuditStringFromMap(rawPaper, "arxiv_id"))
		if err != nil {
			continue
		}
		if version == 0 {
			version = arxivAuditIntFromAny(rawPaper["version"])
		}
		trace.Papers[id] = arxivScreeningPaperRef{
			ArxivID:      id,
			Version:      version,
			Status:       strings.TrimSpace(strings.ToLower(arxivAuditStringFromMap(rawPaper, "status"))),
			Index:        idx,
			Evidence:     arxivAuditEntryMaps(rawPaper["evidence"]),
			ReadState:    strings.TrimSpace(strings.ToLower(arxivAuditStringFromMap(rawPaper, "read_state"))),
			ReadCoverage: arxivAuditMapFromAny(rawPaper["read_coverage"]),
		}
	}
	return trace, len(trace.Papers) > 0
}

func arxivScreeningReportMapFromEnvelope(ctx context.Context, exec ExecContext, env chattypes.ToolResultEnvelope) (map[string]any, bool) {
	result, _ := env.Result.(map[string]any)
	if isArxivScreeningReportMap(result) {
		return result, true
	}
	if exec.Store == nil || strings.TrimSpace(env.ArtifactID) == "" {
		return result, false
	}
	artifact, err := exec.Store.GetArtifact(ctx, strings.TrimSpace(env.ArtifactID))
	if err != nil {
		return result, false
	}
	content, truncated, _, err := readArtifactContentPrefix(exec.CfgDir, artifact, 8<<20)
	if err != nil || truncated {
		return result, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return result, false
	}
	if nested, nestedOK := raw["result"].(map[string]any); nestedOK && isArxivScreeningReportMap(nested) {
		return nested, true
	}
	if isArxivScreeningReportMap(raw) {
		return raw, true
	}
	return result, false
}

func isArxivScreeningReportMap(result map[string]any) bool {
	if result == nil {
		return false
	}
	if schema := strings.TrimSpace(arxivAuditStringFromMap(result, "schema")); schema != "" && schema != arxivAuditSchema {
		return false
	}
	return len(arxivAuditEntryMaps(result["papers"])) > 0
}

func arxivAuditEntryMaps(raw any) []map[string]any {
	switch entries := raw.(type) {
	case []map[string]any:
		return append([]map[string]any(nil), entries...)
	case []any:
		out := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			if m, ok := entry.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func buildArxivAuditQueryViews(auditCtx *arxivAuditContext, rationales []arxivQueryRationaleInput) []map[string]any {
	byCallID := map[string]arxivQueryRationaleInput{}
	for _, rationale := range rationales {
		callID := strings.TrimSpace(rationale.ToolCallID)
		if callID == "" {
			auditCtx.Warnings = append(auditCtx.Warnings, arxivAuditWarning{Code: "missing_query_tool_call_id", Message: "A query rationale omitted tool_call_id."})
			continue
		}
		byCallID[callID] = rationale
	}
	queryIndex := map[string]int{}
	for i, q := range auditCtx.Queries {
		queryIndex[q.ToolCallID] = i
	}
	firstSeenIndex := map[string]int{}
	for id, paper := range auditCtx.Papers {
		firstSeenIndex[id] = paper.FirstSeenIndex
	}

	seen := map[string]struct{}{}
	out := make([]map[string]any, 0, len(auditCtx.Queries))
	for idx, q := range auditCtx.Queries {
		newCount := 0
		duplicateCount := 0
		for _, id := range q.ReturnedIDs {
			if _, ok := seen[id]; ok {
				duplicateCount++
			} else {
				newCount++
				seen[id] = struct{}{}
			}
		}
		row := map[string]any{
			"tool_call_id":    q.ToolCallID,
			"query_number":    idx + 1,
			"args":            cloneAnyMap(q.Args),
			"returned_count":  len(q.ReturnedIDs),
			"new_count":       newCount,
			"duplicate_count": duplicateCount,
			"upstream_total":  q.TotalResults,
			"start":           q.Start,
			"items_per_page":  q.ItemsPerPage,
			"returned_ids":    append([]string(nil), q.ReturnedIDs...),
			"source_kind":     "deterministic",
		}
		if r, ok := byCallID[q.ToolCallID]; ok {
			row["rationale"] = sourcedText(r.Rationale, "llm_judgment")
			if phase := strings.TrimSpace(r.Phase); phase != "" {
				row["phase"] = sourcedText(phase, "llm_judgment")
			}
			derived := make([]map[string]any, 0, len(r.DerivedFromArxivIDs))
			for _, rawID := range r.DerivedFromArxivIDs {
				id, _, err := arxivapi.CanonicalID(rawID)
				sourceKind := "invalid"
				if err == nil && firstSeenIndex[id] >= 0 && firstSeenIndex[id] < idx {
					sourceKind = "validated"
				} else {
					auditCtx.Warnings = append(auditCtx.Warnings, arxivAuditWarning{
						Code:       "invalid_query_lineage",
						Message:    "Query lineage must reference a paper returned by an earlier arXiv search in the same run.",
						ArxivID:    strings.TrimSpace(rawID),
						ToolCallID: q.ToolCallID,
					})
				}
				derived = append(derived, map[string]any{"arxiv_id": idOrRaw(id, rawID), "source_kind": sourceKind})
			}
			row["derived_from"] = derived
		}
		out = append(out, row)
	}
	for callID := range byCallID {
		if _, ok := queryIndex[callID]; !ok {
			auditCtx.Warnings = append(auditCtx.Warnings, arxivAuditWarning{
				Code:       "unknown_query_rationale",
				Message:    "A query rationale referenced an arXiv search tool_call_id that was not found in this run.",
				ToolCallID: callID,
			})
		}
	}
	return out
}

func arxivReadCoverageMap(traces []arxivAuditReadTrace) map[string]any {
	if len(traces) == 0 {
		return nil
	}
	toolCallIDs := make([]string, 0, len(traces))
	pageRanges := make([]map[string]any, 0, len(traces))
	seenPages := map[int]struct{}{}
	pageCount := 0
	pagesReadFallback := 0
	latest := traces[len(traces)-1]
	for _, trace := range traces {
		toolCallIDs = appendUniqueString(toolCallIDs, trace.ToolCallID)
		if trace.PageCount > pageCount {
			pageCount = trace.PageCount
		}
		if trace.PagesRead > 0 {
			pagesReadFallback += trace.PagesRead
		}
		row := map[string]any{
			"tool_call_id": trace.ToolCallID,
			"pages_read":   trace.PagesRead,
		}
		if trace.StartPage > 0 {
			row["start_page"] = trace.StartPage
		}
		if trace.EndPage > 0 {
			row["end_page"] = trace.EndPage
		}
		if trace.PageCount > 0 {
			row["page_count"] = trace.PageCount
		}
		if trace.SourceKind != "" {
			row["content_source_kind"] = trace.SourceKind
		}
		if trace.StartPage > 0 && trace.EndPage >= trace.StartPage {
			for page := trace.StartPage; page <= trace.EndPage; page++ {
				seenPages[page] = struct{}{}
			}
		}
		pageRanges = append(pageRanges, row)
	}
	pagesRead := len(seenPages)
	if pagesRead == 0 {
		pagesRead = pagesReadFallback
	}
	out := map[string]any{
		"source_kind":         "deterministic",
		"read_state":          "read",
		"read_tool_call_ids":  toolCallIDs,
		"latest_tool_call_id": latest.ToolCallID,
		"page_ranges":         pageRanges,
		"pages_read_count":    pagesRead,
	}
	if pageCount > 0 {
		out["page_count"] = pageCount
	}
	if latest.PaperKey != "" {
		out["paper_key"] = latest.PaperKey
	}
	if latest.ExtractState != "" {
		out["extract_state"] = latest.ExtractState
	}
	if latest.ExtractQuality != "" {
		out["extract_quality"] = latest.ExtractQuality
	}
	if latest.SourceKind != "" {
		out["content_source_kind"] = latest.SourceKind
	}
	return out
}

func buildArxivAuditPaperViews(ctx context.Context, manager *arxivapi.Manager, auditCtx *arxivAuditContext, verdicts []arxivVerdictInput) ([]map[string]any, arxivAuditCounts, []arxivAuditWarning) {
	warnings := []arxivAuditWarning{}
	counts := arxivAuditCounts{Unique: len(auditCtx.Papers)}
	for _, q := range auditCtx.Queries {
		counts.Returned += len(q.ReturnedIDs)
	}
	verdictByID := map[string]arxivVerdictInput{}
	for _, verdict := range verdicts {
		id, version, err := arxivapi.CanonicalID(verdict.ArxivID)
		if err != nil {
			warnings = append(warnings, arxivAuditWarning{Code: "invalid_verdict_id", Message: "A verdict referenced an invalid arXiv ID.", ArxivID: strings.TrimSpace(verdict.ArxivID)})
			continue
		}
		paper := auditCtx.Papers[id]
		if paper == nil {
			warnings = append(warnings, arxivAuditWarning{Code: "unknown_verdict_id", Message: "A verdict referenced a paper that was not returned by arXiv in this run.", ArxivID: id})
			continue
		}
		if verdict.Version > 0 {
			version = verdict.Version
		}
		if version > 0 && paper.Version > 0 && version != paper.Version {
			warnings = append(warnings, arxivAuditWarning{Code: "version_mismatch", Message: "A verdict version did not match the returned arXiv paper version.", ArxivID: id})
			continue
		}
		status := normalizeArxivVerdictStatus(verdict.Status)
		if status == "" {
			warnings = append(warnings, arxivAuditWarning{Code: "invalid_verdict_status", Message: "A verdict status must be kept, possible, or screened_out.", ArxivID: id})
			continue
		}
		if _, exists := verdictByID[id]; exists {
			warnings = append(warnings, arxivAuditWarning{Code: "duplicate_verdict", Message: "Multiple verdicts referenced the same arXiv ID; the first valid verdict was used.", ArxivID: id})
			continue
		}
		verdict.Status = status
		verdict.ArxivID = id
		verdictByID[id] = verdict
	}

	out := make([]map[string]any, 0, len(auditCtx.Papers))
	for _, paper := range auditCtx.Papers {
		verdict, hasVerdict := verdictByID[paper.ArxivID]
		status := "not_screened"
		statusSource := "deterministic"
		var reason any
		evidence := []map[string]any{}
		invalidEvidence := []map[string]any{}
		readCoverage := arxivReadCoverageMap(auditCtx.Reads[paper.ArxivID])
		readState := "not_read"
		if len(readCoverage) > 0 {
			readState = "read"
			counts.ReadPapers++
		}
		if hasVerdict {
			status = normalizeArxivVerdictStatus(verdict.Status)
			statusSource = "validated"
			reason = sourcedText(verdict.Reason, "llm_judgment")
			ev, invalid, evWarnings := validateArxivEvidence(ctx, manager, *paper, verdict.Evidence)
			evidence = ev
			invalidEvidence = invalid
			warnings = append(warnings, evWarnings...)
			if (status == "kept" || status == "possible") && readState != "read" {
				warnings = append(warnings, arxivAuditWarning{Code: "positive_verdict_without_read_trace", Message: "Kept or possible paper had no deterministic arxiv_read_paper page-read trace in this run.", ArxivID: paper.ArxivID})
			}
		}
		switch status {
		case "kept":
			counts.Kept++
		case "possible":
			counts.Possible++
		case "screened_out":
			counts.ScreenedOut++
		default:
			counts.NotScreened++
		}
		row := map[string]any{
			"arxiv_id":           paper.ArxivID,
			"version":            paper.Version,
			"title":              paper.Title,
			"summary":            paper.Summary,
			"authors":            append([]string(nil), paper.Authors...),
			"categories":         append([]string(nil), paper.Categories...),
			"primary_category":   paper.PrimaryCategory,
			"published":          paper.Published,
			"updated":            paper.Updated,
			"abs_url":            paper.AbsURL,
			"pdf_url":            paper.PDFURL,
			"downloaded":         paper.Downloaded,
			"found_by":           append([]string(nil), paper.FoundBy...),
			"first_seen_query":   paper.FirstSeen,
			"duplicate_in":       append([]string(nil), paper.DuplicateIn...),
			"status":             status,
			"status_source_kind": statusSource,
			"read_state":         readState,
			"evidence":           evidence,
			"invalid_evidence":   invalidEvidence,
			"source_kind":        "deterministic",
		}
		if len(readCoverage) > 0 {
			row["read_coverage"] = readCoverage
		}
		if reason != nil {
			row["reason"] = reason
		}
		if paper.PaperKey != "" {
			row["paper_key"] = paper.PaperKey
		}
		out = append(out, row)
	}
	return out, counts, warnings
}

func validateArxivEvidence(ctx context.Context, manager *arxivapi.Manager, paper arxivAuditPaper, inputs []arxivEvidenceInput) ([]map[string]any, []map[string]any, []arxivAuditWarning) {
	evidence := []map[string]any{}
	invalid := []map[string]any{}
	warnings := []arxivAuditWarning{}
	textCache := ""
	for _, input := range inputs {
		field := strings.TrimSpace(strings.ToLower(input.SourceField))
		quote := strings.TrimSpace(input.Quote)
		if quote == "" {
			warnings = append(warnings, arxivAuditWarning{Code: "empty_evidence_quote", Message: "Evidence quote was empty.", ArxivID: paper.ArxivID, SourceField: field})
			invalid = append(invalid, invalidEvidenceMap(input, "empty_quote"))
			continue
		}
		source := ""
		switch field {
		case "title":
			source = paper.Title
		case "summary":
			source = paper.Summary
		case "paper_text":
			if textCache == "" {
				text, warn := arxivAuditLoadPaperText(ctx, manager, paper)
				if warn != nil {
					warnings = append(warnings, *warn)
					invalid = append(invalid, invalidEvidenceMap(input, warn.Code))
					continue
				}
				textCache = text
			}
			source = textCache
		default:
			warnings = append(warnings, arxivAuditWarning{Code: "invalid_evidence_source", Message: "Evidence source_field must be title, summary, or paper_text.", ArxivID: paper.ArxivID, SourceField: field})
			invalid = append(invalid, invalidEvidenceMap(input, "invalid_source_field"))
			continue
		}
		start, end, occurrenceIndex, ok, reason := exactEvidenceSpan(source, quote, input.OccurrenceIndex)
		if !ok {
			warnings = append(warnings, arxivAuditWarning{Code: reason, Message: evidenceFailureMessage(reason), ArxivID: paper.ArxivID, SourceField: field})
			invalid = append(invalid, invalidEvidenceMap(input, reason))
			continue
		}
		row := map[string]any{
			"source_kind":  "validated",
			"source_field": field,
			"quote":        quote,
			"start_byte":   start,
			"end_byte":     end,
			"paper_key":    paper.PaperKey,
		}
		if input.OccurrenceIndex != nil {
			row["occurrence_index"] = occurrenceIndex
		}
		evidence = append(evidence, row)
	}
	return evidence, invalid, warnings
}

func arxivAuditLoadPaperText(ctx context.Context, manager *arxivapi.Manager, paper arxivAuditPaper) (string, *arxivAuditWarning) {
	if manager == nil {
		return "", &arxivAuditWarning{Code: "arxiv_manager_unavailable", Message: "Stored paper text could not be checked because the arXiv manager is unavailable.", ArxivID: paper.ArxivID, SourceField: "paper_text"}
	}
	lookup := paper.PaperKey
	if lookup == "" {
		lookup = paper.ArxivID
	}
	offset := 0
	var b strings.Builder
	for {
		window, err := manager.GetTextWindow(lookup, offset, arxivAuditTextWindowChars)
		if err != nil {
			return "", &arxivAuditWarning{Code: "paper_text_unavailable", Message: "Stored paper text was unavailable for evidence validation.", ArxivID: paper.ArxivID, SourceField: "paper_text"}
		}
		b.WriteString(window.Text)
		if b.Len() > arxivAuditMaxPaperText {
			return "", &arxivAuditWarning{Code: "paper_text_too_large", Message: "Stored paper text exceeded the audit validation budget.", ArxivID: paper.ArxivID, SourceField: "paper_text"}
		}
		if !window.HasMore || window.NextOffset == nil {
			break
		}
		if *window.NextOffset <= offset {
			return "", &arxivAuditWarning{Code: "paper_text_window_stalled", Message: "Stored paper text window did not make forward progress.", ArxivID: paper.ArxivID, SourceField: "paper_text"}
		}
		offset = *window.NextOffset
	}
	return b.String(), nil
}

func exactEvidenceSpan(source string, quote string, occurrenceIndex *int) (int, int, int, bool, string) {
	if source == "" {
		return 0, 0, 0, false, "empty_evidence_source"
	}
	positions := []int{}
	start := 0
	for {
		idx := strings.Index(source[start:], quote)
		if idx < 0 {
			break
		}
		pos := start + idx
		positions = append(positions, pos)
		start = pos + len(quote)
	}
	if len(positions) == 0 {
		return 0, 0, 0, false, "evidence_quote_not_found"
	}
	selected := 0
	if occurrenceIndex != nil {
		if len(positions) == 1 {
			// The exact quote is unambiguous. Treat an unnecessary model-supplied
			// occurrence index as advisory so a 1-based "1" does not invalidate
			// otherwise deterministic evidence.
			selected = 0
		} else if *occurrenceIndex < 0 || *occurrenceIndex >= len(positions) {
			return 0, 0, 0, false, "evidence_occurrence_out_of_range"
		} else {
			selected = *occurrenceIndex
		}
	} else if len(positions) > 1 {
		return 0, 0, 0, false, "ambiguous_evidence_quote"
	}
	startByte := positions[selected]
	return startByte, startByte + len(quote), selected, true, ""
}

func evidenceFailureMessage(code string) string {
	switch code {
	case "ambiguous_evidence_quote":
		return "Evidence quote appeared multiple times and needs occurrence_index."
	case "evidence_occurrence_out_of_range":
		return "Evidence occurrence_index was outside the exact-match occurrences."
	case "empty_evidence_source":
		return "Evidence source text was empty."
	default:
		return "Evidence quote did not exactly match the requested source field."
	}
}

func normalizeArxivVerdictStatus(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "kept", "keep", "strong":
		return "kept"
	case "possible", "maybe":
		return "possible"
	case "screened_out", "screened-out", "out", "reject", "rejected":
		return "screened_out"
	default:
		return ""
	}
}

func normalizeArxivRecommendationTier(tier string) string {
	switch strings.TrimSpace(strings.ToLower(tier)) {
	case "":
		return ""
	case "primary", "top", "top_pick", "top-pick", "strong":
		return "primary"
	case "secondary", "consider", "possible":
		return "secondary"
	case "background", "context":
		return "background"
	default:
		return ""
	}
}

func validateArxivRecommendationEvidenceRefs(report arxivScreeningReportTrace, paper arxivScreeningPaperRef, refs []arxivRecommendationEvidenceInput) ([]map[string]any, []arxivAuditWarning) {
	out := []map[string]any{}
	warnings := []arxivAuditWarning{}
	for _, ref := range refs {
		if ref.EvidenceIndex < 0 || ref.EvidenceIndex >= len(paper.Evidence) {
			warnings = append(warnings, arxivAuditWarning{Code: "invalid_evidence_ref", Message: "Recommendation evidence_ref index was outside the linked screening report paper evidence array.", ArxivID: paper.ArxivID, ToolCallID: report.ToolCallID})
			continue
		}
		evidence := paper.Evidence[ref.EvidenceIndex]
		sourceField := strings.TrimSpace(strings.ToLower(ref.SourceField))
		reportSourceField := strings.TrimSpace(strings.ToLower(arxivAuditStringFromMap(evidence, "source_field")))
		if sourceField != "" && reportSourceField != "" && sourceField != reportSourceField {
			warnings = append(warnings, arxivAuditWarning{Code: "evidence_ref_source_mismatch", Message: "Recommendation evidence_ref source_field did not match the linked screening report evidence entry.", ArxivID: paper.ArxivID, ToolCallID: report.ToolCallID, SourceField: sourceField})
			continue
		}
		row := map[string]any{
			"evidence_index":                ref.EvidenceIndex,
			"screening_report_tool_call_id": report.ToolCallID,
			"source_kind":                   "validated",
		}
		if report.ArtifactID != "" {
			row["screening_report_artifact_id"] = report.ArtifactID
		}
		if reportSourceField != "" {
			row["source_field"] = reportSourceField
		}
		out = append(out, row)
	}
	return out, warnings
}

func arxivScreeningReportResultMetadata() map[string]any {
	resultMeta := arxivResultMetadata(ArxivToolScreeningReport, "screening_report")
	resultMeta["audit_version"] = 1
	resultMeta["schema"] = arxivAuditSchema
	return resultMeta
}

func arxivRecommendationsResultMetadata(result map[string]any) map[string]any {
	resultMeta := arxivResultMetadata(ArxivToolRecommendations, "recommendation_set")
	resultMeta["schema"] = arxivRecommendationsSchema
	resultMeta["recommendation_version"] = 1
	resultMeta["screening_report_schema"] = arxivAuditSchema
	if report, ok := firstMapFromAny(result["screening_report"]); ok {
		if toolCallID := strings.TrimSpace(arxivAuditStringFromMap(report, "tool_call_id")); toolCallID != "" {
			resultMeta["screening_report_tool_call_id"] = toolCallID
		}
		if artifactID := strings.TrimSpace(arxivAuditStringFromMap(report, "artifact_id")); artifactID != "" {
			resultMeta["screening_report_artifact_id"] = artifactID
		}
	}
	return resultMeta
}

func arxivRecommendationsArtifactMetadata(result map[string]any, args arxivRecommendationsArgs, resultMetadata map[string]any) map[string]any {
	meta := map[string]any{
		"content_kind": "arxiv_recommendations",
		"schema":       arxivRecommendationsSchema,
		"tool_name":    ArxivToolRecommendations,
		"tool_args":    arxivRecommendationsToolArgs(args),
	}
	if len(resultMetadata) > 0 {
		meta["result_metadata"] = cloneAnyMap(resultMetadata)
		if presentation, ok := resultMetadata["tool_presentation"].(map[string]any); ok && len(presentation) > 0 {
			meta["tool_presentation"] = cloneAnyMap(presentation)
		}
	}
	if report, ok := firstMapFromAny(result["screening_report"]); ok {
		meta["screening_report"] = cloneAnyMap(report)
	}
	return meta
}

func arxivRecommendationsToolArgs(in arxivRecommendationsArgs) map[string]any {
	out := map[string]any{}
	if value := strings.TrimSpace(in.ScreeningReportToolCallID); value != "" {
		out["screening_report_tool_call_id"] = value
	}
	if value := strings.TrimSpace(in.SelectionBasis); value != "" {
		out["selection_basis"] = value
	}
	recommendations := make([]map[string]any, 0, len(in.Recommendations))
	for _, rec := range in.Recommendations {
		row := map[string]any{}
		if value := strings.TrimSpace(rec.ArxivID); value != "" {
			row["arxiv_id"] = value
		}
		if rec.Rank > 0 {
			row["rank"] = rec.Rank
		}
		if value := strings.TrimSpace(rec.Tier); value != "" {
			row["tier"] = value
		}
		if value := strings.TrimSpace(rec.Reason); value != "" {
			row["reason"] = value
		}
		if value := strings.TrimSpace(rec.UseFor); value != "" {
			row["use_for"] = value
		}
		if len(rec.Caveats) > 0 {
			row["caveats"] = cleanArxivStringList(rec.Caveats)
		}
		if len(rec.EvidenceRefs) > 0 {
			refs := make([]map[string]any, 0, len(rec.EvidenceRefs))
			for _, ref := range rec.EvidenceRefs {
				refRow := map[string]any{"evidence_index": ref.EvidenceIndex}
				if value := strings.TrimSpace(ref.SourceField); value != "" {
					refRow["source_field"] = value
				}
				refs = append(refs, refRow)
			}
			row["evidence_refs"] = refs
		}
		recommendations = append(recommendations, row)
	}
	out["recommendations"] = recommendations
	return out
}

func cleanArxivStringList(values []string) []string {
	clean := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		clean = append(clean, value)
	}
	return clean
}

func warningsToMaps(warnings []arxivAuditWarning) []map[string]any {
	out := make([]map[string]any, 0, len(warnings))
	for _, warning := range warnings {
		row := map[string]any{
			"code":    warning.Code,
			"message": warning.Message,
		}
		if warning.ArxivID != "" {
			row["arxiv_id"] = warning.ArxivID
		}
		if warning.ToolCallID != "" {
			row["tool_call_id"] = warning.ToolCallID
		}
		if warning.SourceField != "" {
			row["source_field"] = warning.SourceField
		}
		out = append(out, row)
	}
	return out
}

func invalidEvidenceMap(input arxivEvidenceInput, reason string) map[string]any {
	row := map[string]any{
		"source_field": strings.TrimSpace(strings.ToLower(input.SourceField)),
		"quote":        strings.TrimSpace(input.Quote),
		"source_kind":  "invalid",
		"reason":       reason,
	}
	if input.OccurrenceIndex != nil {
		row["occurrence_index"] = *input.OccurrenceIndex
	}
	return row
}

func sourcedText(value string, sourceKind string) any {
	value = boundAuditText(value, 1200)
	if value == "" {
		return nil
	}
	return map[string]any{"value": value, "source_kind": sourceKind}
}

func sourcedTextList(values []string, sourceKind string) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if row := sourcedText(value, sourceKind); row != nil {
			out = append(out, row.(map[string]any))
		}
	}
	return out
}

func boundAuditText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func derefString(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func firstMapFromAny(value any) (map[string]any, bool) {
	switch m := value.(type) {
	case map[string]any:
		return m, true
	default:
		return nil, false
	}
}

func arxivAuditMapFromAny(value any) map[string]any {
	if m, ok := firstMapFromAny(value); ok {
		return cloneAnyMap(m)
	}
	return nil
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func idOrRaw(id string, raw string) string {
	if strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	return strings.TrimSpace(raw)
}

func arxivAuditStringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return arxivAuditStringFromAny(m[key])
}

func arxivAuditStringFromAny(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case fmt.Stringer:
		return strings.TrimSpace(t.String())
	default:
		return ""
	}
}

func arxivAuditIntFromAny(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	default:
		return 0
	}
}

func arxivAuditBoolFromAny(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	default:
		return false
	}
}

func arxivAuditStringsFromAny(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s := arxivAuditStringFromAny(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
