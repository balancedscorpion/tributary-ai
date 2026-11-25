# arXiv Research Audit Workflow Implementation Plan

Status: proposal
Inputs reviewed: Claude workflow proposal, current `arxiv_*` tool contracts, audit-trail renderer spec, current chat SSE/result envelope flow, and focused backend/frontend/provenance sub-agent reviews.

## Goal

Build a human-centered progressive discovery workflow for arXiv research runs without making the audit trail less trustworthy. The UI should answer four questions at increasing depth:

1. Is the agent on track?
2. What search strategy did it use?
3. What did it find, and why does it think those papers matter?
4. Can the human verify that judgment against the source text?

The current UI already has a strong card -> expand -> open grammar. This plan keeps that grammar and adds a typed research-audit layer on top of existing tool calls, result envelopes, artifacts, and the arXiv paper viewer.

## Non-goals

- Do not replace the generic audit trail. arXiv research audit is a specialized renderer and trace model.
- Do not make arXiv a hidden "research agent" outside normal tool execution. All external calls stay visible as tool calls.
- Do not claim corpus completeness. arXiv search covers public arXiv metadata/preprints, not journal-only literature, private corpora, industry reports, or PDFs that were not downloaded.
- Do not render model judgment as deterministic fact. Relevance tiers, query rationales, and screening reasons are LLM-authored unless later confirmed by a human.
- Do not add env-var switches for this workflow unless deployment constraints force it. Prefer local feature registration and existing config.

## Current System Facts

Relevant current contracts:

- `arxiv_search` returns deterministic arXiv metadata:
  - `total_results`
  - `start`
  - `items_per_page`
  - `entries[]`
  - `entries[].arxiv_id`, `version`, `title`, `summary`, `authors`, `categories`, `primary_category`, dates, `abs_url`, `pdf_url`, `downloaded`, optional `paper_key`
  - `untrusted_note`
  - `result_metadata: { provider:"arxiv", integration:"arxiv", kind:"search_results", readonly:true }`
- The manager copies `result_metadata` and `query_metadata` into persisted tool result envelopes and artifacts.
- The frontend adds `_uiStartedAt`, `_uiEndedAt`, and `_uiRunId` for live tool calls.
- `CollapsedToolsSummary` is the current scanning surface for tool activity.
- `ArxivToolRenderer` is the current rich arXiv inspector.
- `ArxivPaperViewer` can show stored paper text with UTF-8 byte-offset highlights, but abstract/title metadata is rendered separately and is not yet highlight-addressable.
- External arXiv title/abstract/PDF text must be treated as untrusted data, not agent instructions.

Important current limitations:

- `arxiv_search` does not receive or return query rationale, lineage, relevance judgment, or screening verdicts.
- `total_results` is the upstream result count for a query, not the number of unique papers found in the run.
- There is no deterministic "kept" count today.
- There is no deterministic "saturation" signal beyond "this returned page had zero new IDs compared with earlier returned pages".
- There is no backend trace API that aggregates all arXiv calls for a run.
- Existing live UI can derive timing from SSE, but post-hoc replay depends on persisted run events/messages and generated artifacts.

## Prototype Review Decisions

The attached prototype is directionally strong. The implementation should adopt its progressive disclosure shape, but not copy every label or claim literally.

Adopt:

- Funnel summary as the collapsed run line.
- Intent/corpus boundary before results.
- Verbatim query display with rationale and lineage when available.
- Result provenance chips back to query cards.
- Evidence snippets that follow into the reader.
- Auditable exclusions with a user override.
- Working-set tray and export path.
- Same UI model for live watching and post-hoc replay.

Adjust:

- Use `returned` instead of `hits` unless the value is exactly observed returned entries.
- Use `agent-kept`, `agent-possible`, and `agent-screened-out` instead of bare `strong`, `possible`, or `screened out` when the source is LLM judgment.
- Use `0 new in this query` instead of `saturated` unless a real stopping criterion is validated.
- Do not display `sort=relevance` or `cached` chips from the prototype unless the backend records those fields. Current `arxiv_search` has no sort or cache contract.
- Do not say an override "feeds the correction back to the agent" until there is a persisted feedback action or explicit follow-up prompt.

Defer:

- Full trace export until the trace API exists.
- BibTeX export beyond a deterministic best-effort export from observed arXiv metadata.
- Full-text evidence for papers that are not downloaded and extracted locally.

## Information Source Model

Every visible field in the arXiv research audit should carry an internal `source_kind`.

| Source kind | Meaning | UI wording |
|---|---|---|
| `deterministic` | Directly observed from tool args, tool results, run timing, or local store state. | Plain factual labels are allowed. |
| `static` | Product-owned explanatory boundary text. | Plain text is allowed. |
| `llm_judgment` | Authored by the model and not independently proven. | Use "Agent ..." phrasing or judgment styling. |
| `validated` | LLM-authored claim checked against deterministic data. | May be shown as a verified model claim. |
| `human_judgment` | User override or explicit human action. | Show as pinned/kept by user. |
| `invalid` | LLM-authored claim failed validation. | Do not use for counts or highlights; expose in diagnostics. |

### Field Inventory

| Field | Available now? | Source | Capture point | Validation | UI treatment |
|---|---:|---|---|---|---|
| Tool name | Yes | deterministic | `tool_calls` SSE / persisted message | Exact tool name | Normal audit label |
| Verbatim query args | Yes | deterministic | tool call args | JSON schema decode | Mono request line |
| Query start/end/duration | Partly | deterministic | frontend live timestamps, backend events | Compare started/ended event or local timestamp | Timing chip; mark cache only if measured |
| Returned arXiv IDs | Yes | deterministic | `arxiv_search.entries[]` | Normalize arXiv ID/version | Result-set provenance |
| Returned count | Yes | deterministic | `len(entries)` | Count entries | "N returned" |
| Upstream total | Yes | deterministic | `total_results` | Numeric, per query only | "M total upstream" |
| Unique count across run | Derivable | deterministic | aggregate returned IDs across arXiv calls | Canonical ID/version set | Funnel count |
| New count per query | Derivable | deterministic | aggregate returned IDs in order | Compare against prior set | "K new in this page" |
| Duplicate count | Derivable | deterministic | aggregate returned IDs in order | Same canonical set | "D duplicates" |
| Corpus boundary | Yes | static | local copy | Product-owned | One-line trust/corpus notice |
| Concept chips | No | llm_judgment | screening/audit report | None unless product taxonomy exists | "Agent interpreted as ..." |
| Query rationale | No | llm_judgment | screening/audit report or explicit args | Length/schema only | "Agent rationale" |
| Query lineage | No | llm_judgment -> validated | report references previous IDs | Referenced IDs must have appeared earlier | Show only validated links |
| Kept/screened verdict | No | llm_judgment | screening/audit report | Referenced ID must exist in run | "Agent kept", "Agent screened out" |
| Kept count | No | validated | validated screening report | Count valid verdicts only | Do not show before report |
| Evidence quote | No | llm_judgment -> validated | screening report | Exact substring match in named source field | Highlight only when validated |
| Evidence span offsets | No | deterministic after validation | server validator | UTF-8 byte offsets in exact source field | Link to abstract/text location |
| Exclusion reason | No | llm_judgment | screening report | Referenced ID must exist | Show as agent-screened reason |
| Human override | Not yet | human_judgment | UI action persisted locally or via API | Must reference known paper | "Kept by you" |
| Working set | Partly | human_judgment + deterministic | artifact pin/context model | Paper IDs exist | Tray or pinned-paper section |

## Proposed Architecture

The architecture should be additive and staged:

1. A deterministic arXiv run model in the frontend, derived from existing tool calls/results.
2. A backend-validated screening report tool that lets the LLM add strategy, relevance, and evidence without bypassing validation.
3. A durable trace/read model so live and post-hoc views render from the same contract.
4. ArXiv-specific audit rendering and paper-viewer evidence highlighting.

### Stage 1: Deterministic Run Model From Existing Envelopes

Add a frontend builder that groups arXiv tool calls within an assistant turn/run.

Suggested location:

- `components/chat/toolActivity/arxivAuditModel.ts`

Core type:

```ts
export type SourceKind =
  | "deterministic"
  | "static"
  | "llm_judgment"
  | "validated"
  | "human_judgment"
  | "invalid";

export interface ArxivRunModel {
  schema: "arxiv_research_audit.v1";
  runId?: string;
  status: "live" | "complete" | "failed" | "partial";
  corpus: ArxivCorpusBoundary;
  funnel: ArxivFunnel;
  queries: ArxivQueryTrace[];
  papers: ArxivPaperTrace[];
  screening?: ArxivScreeningReportView;
  warnings: ArxivAuditWarning[];
}
```

Stage 1 can render:

- Collapsed summary: `Searched arXiv | 5 queries | 34 returned -> 18 unique`
- Per-query cards:
  - verbatim args
  - returned count
  - new count
  - duplicate count
  - upstream total
  - zero-new page marker
- Paper rows:
  - title, authors, categories, summary
  - provenance chips for queries that returned the paper
  - downloaded/stored state
- Corpus boundary:
  - "arXiv contains preprints and metadata returned by the public arXiv API. It does not cover journal-only papers, private corpora, or non-arXiv industry reports."

Stage 1 must not render:

- kept count
- relevance tiers
- evidence highlights
- query rationales
- "saturated" as a global claim

### Stage 2: LLM Screening Report Tool

Add a new local tool that the model calls after one or more `arxiv_search` calls.

Suggested tool name:

- `arxiv_screening_report`

Purpose:

- It does not call arXiv.
- It reads the current run's prior arXiv search results from store/context.
- It accepts model-authored interpretation, strategy, verdicts, and evidence quotes.
- It validates every referenced paper and evidence quote against prior deterministic results.
- It returns a normalized, validated audit artifact.

Why a tool instead of free-form assistant text:

- It gives the LLM a structured place to declare rationale and relevance.
- It lets the backend validate claims before the UI treats them as audit data.
- It keeps live and post-hoc behavior identical because the report is persisted as a tool result envelope.
- It avoids scraping natural language after the fact, which is unreliable and hard to audit.

Suggested backend locations:

- `server/internal/chat/tools/arxiv_tools.go` for registration.
- A small validator package under `server/internal/arxiv/audit` if validation grows beyond one file.

Current-run collection contract:

- `ExecContext` already carries `Store`, `ThreadID`, and `RunID`.
- MVP can page through `exec.Store.ListMessages(ctx, store.ListMessagesParams{ThreadID: exec.ThreadID, IncludeHidden: true, OrderBy: "seq_asc", Limit: 500})`, advancing `AfterSeq`, then filter messages where `RunID != nil && *RunID == exec.RunID`.
- Bound the scan defensively and return a validation warning if the current run has more messages than the collector budget. Do not silently emit a partial denominator.
- Parse only tool-result envelopes for `arxiv_search` with `result_metadata.kind === "search_results"`.
- Ignore assistant prose and non-arXiv tools when building the validation index.
- Return a structured error if `Store`, `ThreadID`, or `RunID` is missing. The report tool depends on current-run provenance and should not accept self-reported search results in its args.

Suggested argument schema:

```go
type ArxivScreeningReportArgs struct {
	InterpretedRequest string `json:"interpreted_request"`
	Concepts           []string `json:"concepts,omitempty"`
	CorpusGaps         []string `json:"corpus_gaps,omitempty"`
	QueryRationales    []ArxivQueryRationaleInput `json:"query_rationales,omitempty"`
	Verdicts           []ArxivVerdictInput `json:"verdicts"`
}

type ArxivQueryRationaleInput struct {
	ToolCallID            string   `json:"tool_call_id"`
	Phase                 string   `json:"phase,omitempty"`
	Rationale             string   `json:"rationale"`
	DerivedFromArxivIDs   []string `json:"derived_from_arxiv_ids,omitempty"`
}

type ArxivVerdictInput struct {
	ArxivID       string                 `json:"arxiv_id"`
	Version       int                    `json:"version,omitempty"`
	Status        string                 `json:"status"` // kept | possible | screened_out
	Reason        string                 `json:"reason"`
	Evidence      []ArxivEvidenceInput   `json:"evidence,omitempty"`
}

type ArxivEvidenceInput struct {
	SourceField     string `json:"source_field"` // title | summary | paper_text
	Quote           string `json:"quote"`
	OccurrenceIndex *int   `json:"occurrence_index,omitempty"`
}
```

Report fields should preserve their source, not just their value:

```ts
interface SourcedText {
  value: string;
  source_kind: "llm_judgment" | "static" | "validated" | "human_judgment";
}

interface ArxivScreeningReportView {
  interpreted_request?: SourcedText;
  concepts: Array<SourcedText>;
  corpus_gaps: Array<SourcedText>;
  query_rationales: Array<{
    tool_call_id: string;
    rationale: SourcedText;
    phase?: SourcedText;
    derived_from: Array<{
      arxiv_id: string;
      source_kind: "validated" | "invalid";
    }>;
  }>;
  verdicts: Array<{
    arxiv_id: string;
    status: "kept" | "possible" | "screened_out" | "not_screened";
    status_source_kind: "llm_judgment" | "validated" | "human_judgment" | "deterministic";
    reason?: SourcedText;
    evidence: ArxivEvidenceSpan[];
    invalid_evidence: ArxivAuditWarning[];
  }>;
}
```

The validator may upgrade a paper verdict from `llm_judgment` to `validated` only in the narrow sense that the referenced paper exists in this run. It does not prove the relevance judgment is correct. UI copy should still read as `agent-kept` or `agent-screened-out`.

Suggested return shape:

```json
{
  "schema": "arxiv_research_audit.v1",
  "run_id": "run_...",
  "thread_id": "thread_...",
  "corpus": {
    "provider": "arxiv",
    "boundary": "arXiv public metadata and locally downloaded arXiv paper text only.",
    "untrusted_external_data": true
  },
  "funnel": {
    "query_count": 5,
    "returned_count": 34,
    "unique_count": 18,
    "agent_kept_count": 6,
    "agent_possible_count": 4,
    "agent_screened_out_count": 8,
    "not_screened_count": 0
  },
  "queries": [],
  "papers": [],
  "validation_warnings": [],
  "result_metadata": {
    "provider": "arxiv",
    "integration": "arxiv",
    "kind": "screening_report",
    "audit_version": 1,
    "readonly": true
  }
}
```

The tool should add `not_screened` entries for any returned paper that is missing from the model's verdict list. That prevents the UI from implying a full screening denominator when the model omitted papers.

### Stage 3: Durable Trace Read Model

Stage 2 can ship using persisted tool result messages, but the clean architecture target is a durable read model for tool traces.

Suggested API:

- `GET /api/runs/{runId}/trace`

Suggested response:

```ts
interface RunTraceResponse {
  run_id: string;
  thread_id: string;
  events: ToolInvocationTraceV1[];
  artifacts: Array<{
    artifact_id: string;
    tool_call_id?: string;
    tool_name?: string;
    result_metadata?: Record<string, unknown>;
  }>;
}

interface ToolInvocationTraceV1 {
  schema: "tool_invocation_trace.v1";
  tool_call_id: string;
  tool_name: string;
  status: "pending" | "running" | "completed" | "failed" | "cancelled";
  args: unknown;
  started_at?: string;
  ended_at?: string;
  duration_ms?: number;
  result_metadata?: Record<string, unknown>;
  result_summary?: unknown;
}
```

This should be a read-model facade over existing persisted run events/messages first. Only add new storage columns if existing event durability cannot preserve enough data after reload.

The trace API should not duplicate large payloads such as PDFs, full extracted text, or raw XML. It should include bounded metadata and artifact references.

### Stage 4: ArXiv Audit Renderer

Render from `ArxivRunModel`, not raw tool envelopes.

Suggested frontend locations:

- `components/chat/toolActivity/arxivAuditModel.ts`
- `components/chat/toolActivity/ArxivAuditCollapsedPreview.tsx`
- `components/renderers/tool/arxiv/ArxivResearchAuditRenderer.tsx`
- Extend `components/renderers/tool/arxiv/ArxivToolRenderer.tsx` resolver to claim `kind === "screening_report"` and aggregate arXiv search groups.

Primary surfaces:

1. Collapsed run summary in `CollapsedToolsSummary`
   - Example before screening report:
     - `Searching arXiv | 3 queries | 17 returned -> 12 unique`
   - Example after screening report:
     - `Searched arXiv | 5 queries | 34 returned -> 18 unique -> 6 agent-kept`
   - Avoid "34 hits" unless the count is exactly returned entries. Use "returned" for observed entries and "upstream total" for arXiv total.

2. Inspector overview
   - Status/funnel band.
   - Corpus boundary and trust boundary.
   - Concept chips marked as agent interpretation.
   - Query list with deterministic counts and optional agent rationale.
   - Working set of kept/pinned papers.

3. Query detail
   - Verbatim query args in mono.
   - Deterministic returned/new/duplicate/upstream-total stats.
   - Agent rationale if present.
   - Validated lineage chips only.
   - Result list for that query.

4. Paper detail
   - Title/authors/categories.
   - Provenance strip.
   - Agent verdict and reason.
   - Evidence snippets with validation state.
   - "Open in reader" action with transient evidence highlight.

5. Paper reader
   - Existing `ArxivPaperViewer` with support for transient validated evidence highlights.
   - Abstract/title evidence can be highlighted in metadata view.
   - Full-text evidence uses stored text byte offsets and opens the text viewer near the evidence.

## Validation Rules

Validation must run before the UI uses LLM-authored data in counts, highlights, or lineage.

### Paper Reference Validation

- Normalize arXiv IDs from prior `arxiv_search.entries[]`.
- A verdict is valid only if its `arxiv_id` exists in prior returned entries for the same run.
- If `version` is supplied, match the version when available. Otherwise match by canonical arXiv ID.
- Unknown IDs are excluded from counters and rendered only in diagnostics.

### Query Rationale Validation

- `tool_call_id` must match a prior arXiv search call in the same run.
- If `DerivedFromArxivIDs` is present, every ID must have appeared in a prior query, not the same or a later query.
- Invalid lineage links are dropped with a validation warning.
- Rationale text is LLM-authored. It should be rendered as agent reasoning, not as product fact.

### Evidence Quote Validation

Evidence highlighting requires exact substring match. No fuzzy matching for MVP.

Rules:

- `source_field` must be one of `title`, `summary`, or `paper_text`.
- `title` and `summary` validate against the exact strings returned by arXiv metadata.
- `paper_text` validates only when the paper is locally downloaded and extracted text is available.
- `quote` must be non-empty after trimming.
- If the quote occurs once, compute UTF-8 byte start/end offsets.
- If the quote occurs multiple times, require `occurrence_index`. Without it, mark the evidence ambiguous and do not highlight.
- If the quote does not occur, keep the quote in diagnostics but do not render it as evidence.
- The UI may show "Agent evidence could not be verified" in the paper detail, but it must not highlight a failed quote.

Validated evidence shape:

```ts
interface ArxivEvidenceSpan {
  source_kind: "validated";
  source_field: "title" | "summary" | "paper_text";
  quote: string;
  start_byte: number;
  end_byte: number;
  occurrence_index?: number;
  artifact_id?: string;
  paper_key?: string;
}
```

### Count Validation

- `query_count`: number of observed `arxiv_search` calls.
- `returned_count`: sum of observed returned entries across query pages.
- `unique_count`: unique normalized arXiv IDs across observed returned entries.
- `agent_kept_count`: count valid verdicts with `status === "kept"`.
- `agent_possible_count`: count valid verdicts with `status === "possible"`.
- `agent_screened_out_count`: count valid verdicts with `status === "screened_out"`.
- `not_screened_count`: unique returned papers without a valid verdict.
- Do not include invalid/unknown IDs in denominator or kept counts.
- Do not show kept counts before a valid screening report exists.

### Saturation Language

Avoid global saturation claims unless the backend has an explicit search-plan stopping rule.

Allowed:

- `0 new in this returned page`
- `No new papers in this query`
- `Duplicate-heavy query`

Avoid:

- `Saturated`
- `Coverage complete`
- `Search exhausted`

If the agent provides a stopping rationale, render it as `Agent stopping rationale` and keep it in `llm_judgment`.

## UX Workflow

### Level 0: Collapsed Summary

Purpose: answer "is it on track?"

Before results:

- `Searching arXiv | query 2 running`

After search results, before screening:

- `Searching arXiv | 3 queries | 17 returned -> 12 unique | screening pending`

After validated screening:

- `Searched arXiv | 5 queries | 34 returned -> 18 unique -> 6 agent-kept`

Error/partial:

- `Searched arXiv | 4 queries | 1 failed | 21 returned -> 14 unique`

### Level 1: Strategy

Purpose: answer "what was the strategy?"

The inspector opens with:

- corpus boundary
- agent interpretation/concepts if present
- query sequence
- per-query deterministic stats
- optional agent rationale
- validated lineage chips

Visual grammar:

- Query string/args in mono.
- Deterministic stats in neutral chips.
- Agent-authored rationale in an "Agent rationale" row.
- Lineage chips link to prior paper rows only after validation.
- Zero-new queries stay visible because they explain stopping behavior.

### Level 2: Findings

Purpose: answer "what did it find, and why does the agent think it is relevant?"

Paper rows show:

- title, authors, category/date
- provenance chips: `Q1`, `Q3`, `exact ID lookup`
- agent verdict: `agent-kept`, `agent-possible`, `agent-screened-out`, `not screened`
- one-line agent reason if valid paper reference
- evidence snippet only if exact-match validated
- override action: `Keep`

Exclusions should be auditable:

- Screened-out papers are shown in a collapsible section, not discarded.
- A `Keep` action changes local/user state to `human_judgment`.
- If no persistence endpoint exists initially, the action can pin the paper in UI state and attach it to the current composer context using existing artifact/pin mechanics. It must not claim the model learned from the correction unless a follow-up prompt/action is sent.

### Level 3: Verification

Purpose: answer "is that judgment actually true?"

Opening evidence:

- For `title` or `summary`, open paper metadata/preview and highlight the exact evidence span.
- For `paper_text`, require a stored paper. Open `ArxivPaperViewer` in text mode near `max(0, start_byte - contextBytes)` and apply a transient highlight.
- If the paper is not stored, offer the existing download/save path before full-text verification.

The same evidence quote should be visible:

- in the paper row
- in paper detail
- in reader highlight

Do not write agent evidence to `useArxivHighlights` localStorage. Agent evidence is transient audit context. User-created highlights remain persistent user annotations.

## Provenance Strip

Every paper detail should have a compact provenance strip.

Recommended fields:

- `Found by Q1, Q3`
- `First seen in Q1`
- `Returned as duplicate in Q3` when applicable
- `Agent-kept` or `Not screened`
- `Evidence verified` or `No verified evidence`
- `Stored locally` or `Metadata only`

Implementation details:

- Derive query chips from deterministic `queries[].returned_ids`.
- Link chips to query cards in the inspector.
- Preserve provenance when a paper is pinned or attached to chat.
- If the user keeps an excluded paper, add `Kept by you` without overwriting the original agent verdict.
- BibTeX export should be generated deterministically from observed metadata (`arxiv_id`, title, authors, year/date, categories, URL). If required fields are missing, include the arXiv ID and omit the field rather than asking the model to invent bibliographic data.

## Exact-Match Evidence Highlighting

The current text highlighter is suitable for stored full text because it uses UTF-8 byte offsets. It needs a transient-evidence path and separate metadata-field highlighting.

Suggested frontend changes:

- Add `transientHighlights?: ArxivHighlight[]` to `ArxivPaperViewer`.
- Add `initialEvidence?: ArxivEvidenceSpan` or `initialEvidenceId?: string`.
- For `paper_text` evidence, open text mode and load a window beginning before the evidence start.
- After text loads, scroll to the highlighted span.
- For `title` and `summary`, add metadata-level highlight rendering using field-relative UTF-8 byte offsets.

Important behavior:

- User highlights persist through `useArxivHighlights`.
- Agent evidence does not persist as user highlight data.
- If exact source text changed or is unavailable, render the quote as unverified and do not highlight.
- Do not style external text as Markdown or assistant speech.

## Live and Post-hoc States

The same `ArxivRunModel` builder should handle live SSE and reloaded messages.

| State | Trigger | UI |
|---|---|---|
| No arXiv calls | no arXiv tool group | no specialized arXiv audit |
| Search pending | `arxiv_search` tool call without result | show query args and running state |
| Search result received | `arxiv_search` result envelope | update returned/new/unique counts |
| Screening pending | `arxiv_screening_report` call without result | show deterministic audit plus "screening pending" |
| Screening valid | validated report result | show verdicts, kept count, evidence |
| Screening missing | run complete without report | show "not screened" for all returned papers |
| Screening invalid | report failed validation | show deterministic audit plus validation warnings |
| Run failed/cancelled | terminal run state | preserve partial deterministic trace |
| Reloaded | fetched persisted messages/trace | render same model with no pulsing/live-only decoration |

## Security and Trust Boundary

- arXiv metadata and extracted text are untrusted external data.
- Render title/abstract/text as data, never as Markdown instructions.
- Keep the existing untrusted notice visible in arXiv audit surfaces.
- LLM rationales and reasons are assistant-authored text. Render them separately from external text.
- Do not pass raw PDF bytes or unbounded extracted text through the audit payload.
- The screening report validator should bound string lengths for rationales/reasons/evidence to prevent oversized artifacts.
- If a model includes prompt-injection-like text from an abstract in a reason, it remains text, not executable UI behavior.

## Implementation Sequence

### Phase 1: Deterministic Audit UI

Backend:

- No required backend change.

Frontend:

- Add `arxivAuditModel.ts`.
- Add arXiv-specific summary generation in `resultSummary.ts` or a parallel registry hook.
- Add collapsed preview to `inlinePreviewRegistry.tsx`.
- Extend `ArxivToolRenderer` or add `ArxivResearchAuditRenderer` for grouped search-result artifacts.
- Render corpus boundary and deterministic query/result provenance.

Acceptance:

- Existing arXiv search results show a funnel based only on observed entries.
- Reloaded runs produce the same deterministic counts.
- No kept/relevance/evidence UI appears without a screening report.

### Phase 2: Validated Screening Report Tool

Backend:

- Register `arxiv_screening_report`.
- Give the tool access to current run/thread context through `ExecContext`.
- Load prior arXiv search tool results for the current run.
- Validate paper references, query references, lineage, verdict denominator, and evidence quotes.
- Return `result_metadata.kind === "screening_report"`.

Frontend:

- Merge screening report into `ArxivRunModel`.
- Render agent interpretation, rationales, verdicts, exclusions, and validation warnings.
- Add kept/possible/screened counts only from valid report data.
- Add tool guidance so the model calls `arxiv_screening_report` after multi-query arXiv research runs, with instructions to quote exact evidence from title/summary or downloaded text.

Acceptance:

- Unknown paper IDs do not affect counts.
- Missing verdicts become `not_screened`.
- Invalid evidence quote shows a warning and no highlight.
- Query lineage only appears for valid prior IDs.

### Phase 3: Evidence Reader Integration

Backend:

- For title/summary, offsets can be computed in the screening validator.
- For `paper_text`, validator fetches local extracted text when available and computes UTF-8 offsets.
- Return `paper_key` and field-relative byte offsets for validated evidence.

Frontend:

- Add transient evidence highlight support to `ArxivPaperViewer`.
- Add title/summary field-level highlighting.
- Wire paper-row evidence links into the viewer.

Acceptance:

- Clicking a validated abstract evidence quote opens the paper detail with that exact abstract span highlighted.
- Clicking a validated full-text evidence quote opens stored text near the quote with a transient highlight.
- Agent evidence does not create persistent user highlights.

### Phase 4: Durable Trace API

Backend:

- Add `GET /api/runs/{runId}/trace` as a read model over persisted events/messages.
- Ensure tool call args, result metadata, started/ended timestamps, and artifact IDs survive reload.
- Consider promoting tool invocation start/end to typed durable events if current persistence is insufficient.

Frontend:

- Prefer trace API for post-hoc audit loading when available.
- Fall back to existing message/envelope derivation while migrating.

Acceptance:

- Live run and replayed run produce the same arXiv audit model.
- A completed run can be audited without an active SSE subscriber.

## Test Plan

Backend unit tests:

- `arxiv_screening_report` rejects/diagnoses unknown IDs.
- Missing verdicts are filled as `not_screened`.
- Query lineage cannot reference papers first seen in later queries.
- Evidence quote exact-match computes UTF-8 byte offsets correctly.
- Duplicate quote without `occurrence_index` is marked ambiguous and not highlighted.
- `paper_text` evidence is invalid when no stored extracted text exists.
- Oversized rationale/reason/evidence strings are bounded.

Frontend unit tests:

- `ArxivRunModel` computes returned/new/duplicate/unique counts across repeated IDs.
- Collapsed summary omits kept count before screening.
- Screening report merge only counts valid verdicts.
- Invalid evidence renders warning state, not highlight state.
- Provenance strip preserves first-seen and duplicate query chips.
- User override adds `human_judgment` without deleting agent verdict.

Frontend interaction tests:

- Collapsed arXiv group opens inspector.
- Query chip jumps to query detail.
- Paper provenance chip jumps back to query.
- Evidence link opens paper viewer with the expected transient highlight.
- Reloaded run shows the same counts as live run.

Manual QA:

- Run an arXiv search with overlapping queries and verify duplicate counts.
- Run a screening report with one intentionally wrong quote and verify no highlight.
- Keep a screened-out paper and verify it appears in the working set as user-kept.
- Confirm external abstract text is not rendered as Markdown or assistant speech.

## Architecture Review

Clean boundaries:

- Deterministic search facts stay owned by `arxiv_search` and the trace builder.
- LLM strategy/relevance stays in a dedicated screening report tool.
- Validation stays server-side, close to persisted run data and local arXiv text access.
- Rendering consumes a typed `ArxivRunModel`, not ad hoc raw JSON.
- The paper viewer remains the verification surface; the audit inspector does not become a separate PDF/text reader.

Risk controls:

- No "kept" count without validated screening.
- No evidence highlight without exact match.
- No hidden corpus-completeness claim.
- No untracked model-authored lineage.
- No permanent user annotation created by transient agent evidence.

## Open Decisions

1. Should `arxiv_screening_report` be automatically encouraged by the system prompt after multi-query arXiv runs, or only available for explicit research/audit mode?
2. Should user overrides persist to backend as run feedback, or initially use existing artifact pin/attach state?
3. Should the trace API be implemented before or after the screening report? The report can work first via persisted messages, but the trace API is cleaner long term.
4. Should evidence matching support normalized whitespace in a later phase? MVP should require exact source string matching to avoid false highlights.
5. Should "concept chips" remain free-text model output, or should they map to a controlled taxonomy for stronger consistency?

## Recommended MVP Scope

Ship in this order:

1. Deterministic grouped arXiv audit model and funnel summary.
2. Validated `arxiv_screening_report` tool with query rationales, verdicts, and exact-match title/summary evidence.
3. Provenance strip and exclusion override using existing pin/attach mechanics.
4. Full-text evidence highlighting for downloaded papers.
5. Durable trace API for replay/post-hoc audit hardening.

This sequence gives users immediate clarity on search breadth and deduplication, then adds model judgment only once it can be validated and safely rendered.
