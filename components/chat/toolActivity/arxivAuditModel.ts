import type { ToolCall, ToolResultEnvelope } from '@/types';

export type ArxivSourceKind =
  | 'deterministic'
  | 'static'
  | 'llm_judgment'
  | 'validated'
  | 'human_judgment'
  | 'invalid';

export interface ArxivSourcedText {
  value: string;
  source_kind: ArxivSourceKind;
}

export interface ArxivEvidenceSpan {
  source_kind: 'validated';
  source_field: 'title' | 'summary' | 'paper_text';
  quote: string;
  start_byte: number;
  end_byte: number;
  occurrence_index?: number;
  paper_key?: string;
}

export interface ArxivAuditWarning {
  code: string;
  message: string;
  arxiv_id?: string;
  tool_call_id?: string;
  source_field?: string;
}

export interface ArxivReadRange {
  tool_call_id?: string;
  start_page?: number;
  end_page?: number;
  pages_read?: number;
  page_count?: number;
  content_source_kind?: string;
}

export interface ArxivReadCoverage {
  source_kind: 'deterministic';
  read_state: 'read';
  read_tool_call_ids: string[];
  latest_tool_call_id?: string;
  page_ranges: ArxivReadRange[];
  pages_read_count?: number;
  page_count?: number;
  paper_key?: string;
  extract_state?: string;
  extract_quality?: string;
  content_source_kind?: string;
}

type ArxivReadCoverageEnvelope = {
  arxiv_id: string;
  coverage: ArxivReadCoverage;
  title?: string;
  paper_key?: string;
};

export interface ArxivQueryTrace {
  tool_call_id: string;
  query_number: number;
  args: Record<string, unknown>;
  returned_count: number;
  new_count: number;
  duplicate_count: number;
  upstream_total?: number;
  start?: number;
  items_per_page?: number;
  returned_ids: string[];
  source_kind: 'deterministic';
  rationale?: ArxivSourcedText;
  phase?: ArxivSourcedText;
  derived_from?: Array<{ arxiv_id: string; source_kind: 'validated' | 'invalid' }>;
}

export interface ArxivPaperTrace {
  arxiv_id: string;
  version?: number;
  title: string;
  summary: string;
  authors: string[];
  categories: string[];
  primary_category?: string;
  published?: string;
  updated?: string;
  abs_url?: string;
  pdf_url?: string;
  downloaded?: boolean;
  paper_key?: string;
  found_by: string[];
  first_seen_query?: string;
  duplicate_in: string[];
  status: 'kept' | 'possible' | 'screened_out' | 'not_screened';
  status_source_kind: ArxivSourceKind;
  read_state: 'read' | 'not_read';
  read_coverage?: ArxivReadCoverage;
  reason?: ArxivSourcedText;
  evidence: ArxivEvidenceSpan[];
  invalid_evidence: ArxivAuditWarning[];
  source_kind: 'deterministic';
}

export interface ArxivFunnel {
  query_count: number;
  returned_count: number;
  unique_count: number;
  read_paper_count?: number;
  agent_kept_count?: number;
  agent_possible_count?: number;
  agent_screened_out_count?: number;
  not_screened_count?: number;
}

export interface ArxivRunModel {
  schema: 'arxiv_research_audit.v1';
  status: 'live' | 'complete' | 'failed' | 'partial';
  corpus: {
    provider: 'arxiv';
    boundary: string;
    source_kind: 'static';
    untrusted_external_data: true;
  };
  funnel: ArxivFunnel;
  queries: ArxivQueryTrace[];
  papers: ArxivPaperTrace[];
  interpreted_request?: ArxivSourcedText;
  concepts: ArxivSourcedText[];
  corpus_gaps: ArxivSourcedText[];
  validation_warnings: ArxivAuditWarning[];
  has_screening_report: boolean;
}

export interface ArxivAuditInput {
  toolCalls: ToolCall[];
  envelopesByCallId?: Map<string, ToolResultEnvelope | undefined> | Record<string, ToolResultEnvelope | undefined>;
  status?: ArxivRunModel['status'];
}

export type ArxivQueryOutcome = 'pending' | 'results' | 'paginated' | 'saturated' | 'starved';

const ARXIV_AUDIT_SCHEMA = 'arxiv_research_audit.v1' as const;

function isRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function getEnvelope(
  envelopes: ArxivAuditInput['envelopesByCallId'],
  callId: string,
): ToolResultEnvelope | undefined {
  if (!envelopes || !callId) return undefined;
  if (envelopes instanceof Map) return envelopes.get(callId);
  return envelopes[callId];
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

function boolValue(value: unknown): boolean | undefined {
  return typeof value === 'boolean' ? value : undefined;
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.map((item) => stringValue(item)).filter(Boolean);
}

function plural(value: number, singular: string, pluralForm = `${singular}s`): string {
  return `${value.toLocaleString()} ${value === 1 ? singular : pluralForm}`;
}

function normalizeArxivId(value: unknown): string {
  let text = stringValue(value).toLowerCase();
  text = text.replace(/^arxiv:/, '');
  try {
    const url = new URL(text);
    if (url.hostname.endsWith('arxiv.org')) {
      text = url.pathname.replace(/^\/(?:abs|pdf)\//, '').replace(/\.pdf$/, '');
    }
  } catch {
    // Not a URL; continue with plain ID.
  }
  text = text.replace(/\/+$/, '');
  text = text.replace(/v\d+$/i, '');
  return text;
}

function resultKind(envelope?: ToolResultEnvelope): string {
  const meta = envelope?.result_metadata;
  return stringValue(meta?.kind);
}

function isArxivSearchEnvelope(envelope?: ToolResultEnvelope): boolean {
  return Boolean(
    envelope
      && envelope.ok !== false
      && (envelope.tool_name === 'arxiv_search' || resultKind(envelope) === 'search_results')
      && resultKind(envelope) === 'search_results',
  );
}

function isArxivScreeningEnvelope(envelope?: ToolResultEnvelope): boolean {
  if (!envelope || envelope.ok === false) return false;
  const kind = resultKind(envelope);
  const result = envelope.result;
  return (
    envelope.tool_name === 'arxiv_screening_report'
    || kind === 'screening_report'
    || (isRecord(result) && result.schema === ARXIV_AUDIT_SCHEMA)
  );
}

function normalizeEvidence(value: unknown): ArxivEvidenceSpan[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!isRecord(item) || item.source_kind !== 'validated') return [];
    const sourceField = stringValue(item.source_field) as ArxivEvidenceSpan['source_field'];
    if (!['title', 'summary', 'paper_text'].includes(sourceField)) return [];
    const quote = stringValue(item.quote);
    const start = numberValue(item.start_byte);
    const end = numberValue(item.end_byte);
    if (!quote || start === undefined || end === undefined || end <= start) return [];
    return [{
      source_kind: 'validated' as const,
      source_field: sourceField,
      quote,
      start_byte: start,
      end_byte: end,
      occurrence_index: numberValue(item.occurrence_index),
      paper_key: stringValue(item.paper_key) || undefined,
    }];
  });
}

function normalizeWarning(value: unknown): ArxivAuditWarning[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!isRecord(item)) return [];
    const code = stringValue(item.code);
    const message = stringValue(item.message);
    if (!code && !message) return [];
    return [{
      code: code || 'validation_warning',
      message: message || code,
      arxiv_id: stringValue(item.arxiv_id) || undefined,
      tool_call_id: stringValue(item.tool_call_id) || undefined,
      source_field: stringValue(item.source_field) || undefined,
    }];
  });
}

function normalizeReadCoverage(value: unknown): ArxivReadCoverage | undefined {
  if (!isRecord(value)) return undefined;
  const readState = stringValue(value.read_state);
  if (readState && readState !== 'read') return undefined;
  const ranges = Array.isArray(value.page_ranges)
    ? value.page_ranges.filter(isRecord).map((range): ArxivReadRange => ({
        tool_call_id: stringValue(range.tool_call_id) || undefined,
        start_page: numberValue(range.start_page),
        end_page: numberValue(range.end_page),
        pages_read: numberValue(range.pages_read),
        page_count: numberValue(range.page_count),
        content_source_kind: stringValue(range.content_source_kind) || undefined,
      }))
    : [];
  const callIds = stringList(value.read_tool_call_ids);
  const latestToolCallId = stringValue(value.latest_tool_call_id);
  if (latestToolCallId && !callIds.includes(latestToolCallId)) callIds.push(latestToolCallId);
  const pagesReadCount = numberValue(value.pages_read_count);
  if (!callIds.length && !ranges.length && pagesReadCount === undefined) return undefined;
  return {
    source_kind: 'deterministic',
    read_state: 'read',
    read_tool_call_ids: callIds,
    latest_tool_call_id: latestToolCallId || undefined,
    page_ranges: ranges,
    pages_read_count: pagesReadCount,
    page_count: numberValue(value.page_count),
    paper_key: stringValue(value.paper_key) || undefined,
    extract_state: stringValue(value.extract_state) || undefined,
    extract_quality: stringValue(value.extract_quality) || undefined,
    content_source_kind: stringValue(value.content_source_kind) || undefined,
  };
}

function readCoverageFromEnvelope(envelope: ToolResultEnvelope): ArxivReadCoverageEnvelope | null {
  if (envelope.ok === false || resultKind(envelope) !== 'paper_pages') return null;
  const result = isRecord(envelope.result) ? envelope.result : {};
  const pageWindow = isRecord(result.paper_pages) ? result.paper_pages : null;
  if (!pageWindow) return null;
  const id = normalizeArxivId(pageWindow.arxiv_id);
  if (!id) return null;
  const startPage = numberValue(pageWindow.start_page);
  const endPage = numberValue(pageWindow.end_page);
  const pages = Array.isArray(pageWindow.pages) ? pageWindow.pages : [];
  const pagesRead = pages.length || (startPage && endPage && endPage >= startPage ? endPage - startPage + 1 : undefined);
  const toolCallId = stringValue(envelope.tool_call_id);
  const coverage = normalizeReadCoverage({
    source_kind: 'deterministic',
    read_state: 'read',
    read_tool_call_ids: toolCallId ? [toolCallId] : [],
    latest_tool_call_id: toolCallId,
    pages_read_count: pagesRead,
    page_count: numberValue(pageWindow.page_count),
    paper_key: stringValue(pageWindow.paper_key) || undefined,
    extract_state: stringValue(pageWindow.extract_state) || undefined,
    extract_quality: stringValue(pageWindow.extract_quality) || undefined,
    content_source_kind: stringValue(pageWindow.source_kind) || undefined,
    page_ranges: [{
      tool_call_id: toolCallId || undefined,
      start_page: startPage,
      end_page: endPage,
      pages_read: pagesRead,
      page_count: numberValue(pageWindow.page_count),
      content_source_kind: stringValue(pageWindow.source_kind) || undefined,
    }],
  });
  return coverage ? {
    arxiv_id: id,
    coverage,
    title: stringValue(pageWindow.title) || undefined,
    paper_key: stringValue(pageWindow.paper_key) || undefined,
  } : null;
}

/**
 * Union read coverage across multiple read calls for one paper, mirroring the
 * backend auditor (which unions tool_call_ids, accumulates page_ranges, and
 * counts distinct pages). A plain overwrite would make the live pre-report
 * card disagree with the post-report card whenever a paper is read twice.
 */
function mergeReadCoverage(
  previous: ArxivReadCoverage | undefined,
  next: ArxivReadCoverage,
): ArxivReadCoverage {
  if (!previous) return next;
  const callIds = Array.from(new Set([...previous.read_tool_call_ids, ...next.read_tool_call_ids]));
  const ranges: ArxivReadRange[] = [...previous.page_ranges];
  next.page_ranges.forEach((range) => {
    const duplicate =
      range.tool_call_id && ranges.some((existing) => existing.tool_call_id === range.tool_call_id);
    if (!duplicate) ranges.push(range);
  });
  const seenPages = new Set<number>();
  let spanlessPages = 0;
  ranges.forEach((range) => {
    if (
      range.start_page !== undefined
      && range.end_page !== undefined
      && range.end_page >= range.start_page
    ) {
      for (let page = range.start_page; page <= range.end_page; page += 1) seenPages.add(page);
    } else if (typeof range.pages_read === 'number') {
      spanlessPages += range.pages_read;
    }
  });
  const mergedPagesRead = seenPages.size + spanlessPages;
  const pageCount = Math.max(previous.page_count ?? 0, next.page_count ?? 0) || undefined;
  return {
    ...next,
    read_tool_call_ids: callIds,
    latest_tool_call_id: next.latest_tool_call_id || previous.latest_tool_call_id,
    page_ranges: ranges,
    pages_read_count: mergedPagesRead > 0 ? mergedPagesRead : next.pages_read_count ?? previous.pages_read_count,
    page_count: pageCount,
    paper_key: next.paper_key || previous.paper_key,
    extract_state: next.extract_state || previous.extract_state,
    extract_quality: next.extract_quality || previous.extract_quality,
    content_source_kind: next.content_source_kind || previous.content_source_kind,
  };
}

/* ------------------------------------------------------------------ */
/* Read register (spec: "No invented motive")                          */
/* ------------------------------------------------------------------ */

/** Why the model opened the paper. Emitted by the backend (reserved today);
    never inferred from anything else — an absent motive stays absent. */
export type ArxivReadMotive = 'explicit' | 'screening' | 'scan';

/** Rendering register for a read. `findings` is the purposeful register
    (looking-for/found, evidence, verification); `notes` is the scan register
    (low-commitment observations — "noticed", never "concluded"). */
export type ArxivReadRegister = 'findings' | 'notes';

export interface ArxivReadNote {
  tag: string;
  text: string;
  source_kind: 'llm_judgment';
}

export interface ArxivChaseCandidate {
  ref: string;
  arxiv_id?: string;
  why?: string;
  source_kind: 'llm_judgment';
}

export interface ArxivReadInsight {
  /** Present only when the backend emitted it. */
  motive?: ArxivReadMotive;
  register: ArxivReadRegister;
  /** True when the register comes from emitted trace data (motive, register,
      or a scan purpose tag / notes-only shape) rather than the findings
      default. Callers must not style an implicit register as a declared one. */
  register_explicit: boolean;
  /** Display tag (SKIM / TARGETED / VERIFY / FULL READ / SCAN…), '' if absent. */
  purpose_tag: string;
  purpose: string;
  /** One model sentence: the finding for `findings`, the observation for `notes`. */
  takeaway: string;
  /** One-line "what is this paper", emitted once at first read. */
  paper_gloss: string;
  notes: ArxivReadNote[];
  chase_candidates: ArxivChaseCandidate[];
  /** Read-level spans captured by the model. Captured ≠ verified: these stay
      "captured · unverified" until a validator locates them. */
  captured_span_count: number;
}

function normalizeReadNotes(value: unknown): ArxivReadNote[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (typeof item === 'string') {
      const text = item.trim();
      return text ? [{ tag: '', text, source_kind: 'llm_judgment' as const }] : [];
    }
    if (!isRecord(item)) return [];
    const text = stringValue(item.text) || stringValue(item.note) || stringValue(item.value);
    if (!text) return [];
    return [{ tag: stringValue(item.tag), text, source_kind: 'llm_judgment' as const }];
  });
}

function normalizeChaseCandidates(value: unknown): ArxivChaseCandidate[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!isRecord(item)) return [];
    const ref = stringValue(item.ref) || stringValue(item.title) || stringValue(item.label);
    const id = normalizeArxivId(item.arxiv_id ?? item.id);
    const why = stringValue(item.why) || stringValue(item.reason);
    if (!ref && !id) return [];
    return [{
      ref: ref || `arXiv:${id}`,
      arxiv_id: id || undefined,
      why: why || undefined,
      source_kind: 'llm_judgment' as const,
    }];
  });
}

function normalizeReadMotive(value: unknown): ArxivReadMotive | undefined {
  const motive = stringValue(value).toLowerCase();
  return motive === 'explicit' || motive === 'screening' || motive === 'scan' ? motive : undefined;
}

function firstStringField(sources: Array<Record<string, any>>, keys: string[]): string {
  for (const source of sources) {
    for (const key of keys) {
      const value = stringValue(source[key]);
      if (value) return value;
    }
  }
  return '';
}

/**
 * Normalize the model-authored reading layer from any mix of carriers (SSE
 * progress event, envelope result, paper_pages/text_window object, screening
 * report row). All fields are reserved backend emissions: this never invents
 * a motive, gloss, or note — it only merges what was actually emitted, first
 * carrier wins per field.
 */
export function arxivReadInsightFromSources(...rawSources: unknown[]): ArxivReadInsight {
  const sources = rawSources.filter(isRecord);
  const motive = sources.map((source) => normalizeReadMotive(source.motive ?? source.read_motive)).find(Boolean);
  const declaredRegister = stringValue(
    sources.map((source) => stringValue(source.register)).find(Boolean),
  ).toLowerCase();
  const purposeTagRaw = firstStringField(sources, ['purpose_tag', 'purposeTag']);
  const purpose = firstStringField(sources, ['purpose']);
  const takeaway = firstStringField(sources, ['takeaway', 'observation']);
  const paperGloss = firstStringField(sources, ['paper_gloss', 'paperGloss', 'gloss']);
  const notes = sources.map((source) => normalizeReadNotes(source.notes)).find((list) => list.length > 0) || [];
  const chases = sources
    .map((source) => normalizeChaseCandidates(source.chase_candidates ?? source.chaseCandidates))
    .find((list) => list.length > 0) || [];
  const capturedSpans = sources
    .map((source) => (Array.isArray(source.spans) ? source.spans.length : 0))
    .find((count) => count > 0) || 0;

  const purposeTagKey = purposeTagRaw.toLowerCase().replace(/[_-]+/g, ' ').trim();
  let register: ArxivReadRegister = 'findings';
  let registerExplicit = false;
  if (declaredRegister === 'notes' || declaredRegister === 'scan' || declaredRegister === 'findings') {
    register = declaredRegister === 'findings' ? 'findings' : 'notes';
    registerExplicit = true;
  } else if (motive) {
    register = motive === 'scan' ? 'notes' : 'findings';
    registerExplicit = true;
  } else if (purposeTagKey === 'scan') {
    register = 'notes';
    registerExplicit = true;
  } else if (notes.length > 0 && !purpose && !takeaway && capturedSpans === 0) {
    // Scan-only reads may omit the findings layer entirely (spec §6); a
    // notes-only emission is that shape, deterministically.
    register = 'notes';
    registerExplicit = true;
  }

  return {
    motive,
    register,
    register_explicit: registerExplicit,
    purpose_tag: purposeTagKey.toUpperCase(),
    purpose,
    takeaway,
    paper_gloss: paperGloss,
    notes,
    chase_candidates: chases,
    captured_span_count: capturedSpans,
  };
}

/** True when this read may render the findings layer (evidence excerpts,
    looking-for/found framing, screening rationale). A scan never may — a scan
    rendering scan-notes beats a scan rendering manufactured findings. */
export function arxivReadShowsFindings(insight: ArxivReadInsight): boolean {
  return insight.register === 'findings';
}

export interface ArxivReadRegisterLabels {
  purposeLabel: string;
  insightLabel: string;
  notesHeading: string;
  registerNote: string;
}

export function arxivReadRegisterLabels(register: ArxivReadRegister): ArxivReadRegisterLabels {
  if (register === 'notes') {
    return {
      purposeLabel: 'Scanning',
      insightLabel: 'Noticed',
      notesHeading: 'noticed · low-commitment',
      registerNote: 'observations, not conclusions',
    };
  }
  return {
    purposeLabel: 'Looking for',
    insightLabel: 'Found',
    notesHeading: 'noticed',
    registerNote: '',
  };
}

export interface ArxivReadDetailCountsInput {
  insight: ArxivReadInsight;
  /** Validated evidence spans joined from the screening trace. */
  verifiedEvidenceCount?: number;
  /** Source flags (degraded extract, truncated window, invalid evidence…). */
  flagCount?: number;
}

/**
 * Counts for the indexed Details layer ("Details — 2 evidence · 3 notes…").
 * Counts are information scent, not decoration: zero-count parts are omitted,
 * and the evidence part is omitted entirely in the notes register so a scan
 * never advertises findings it does not have.
 */
export function arxivReadDetailCountParts(input: ArxivReadDetailCountsInput): string[] {
  const { insight } = input;
  const parts: string[] = [];
  if (arxivReadShowsFindings(insight)) {
    const verified = input.verifiedEvidenceCount ?? 0;
    if (verified > 0) parts.push(plural(verified, 'evidence span'));
    if (insight.captured_span_count > 0) {
      parts.push(`${plural(insight.captured_span_count, 'span')} captured`);
    }
  }
  if (insight.notes.length > 0) parts.push(plural(insight.notes.length, 'note'));
  if (insight.chase_candidates.length > 0) parts.push(plural(insight.chase_candidates.length, 'chase candidate'));
  if ((input.flagCount ?? 0) > 0) parts.push(plural(input.flagCount!, 'source flag'));
  return parts;
}

function normalizeSourceKind(value: unknown, fallbackKind: ArxivSourceKind): ArxivSourceKind {
  const kind = stringValue(value) as ArxivSourceKind;
  return ['deterministic', 'static', 'llm_judgment', 'validated', 'human_judgment', 'invalid'].includes(kind)
    ? kind
    : fallbackKind;
}

function normalizeSourcedText(value: unknown, fallbackKind: ArxivSourceKind): ArxivSourcedText | undefined {
  if (typeof value === 'string') {
    const text = value.trim();
    return text ? { value: text, source_kind: fallbackKind } : undefined;
  }
  if (!isRecord(value)) return undefined;
  const text = stringValue(value.value);
  if (!text) return undefined;
  return { value: text, source_kind: normalizeSourceKind(value.source_kind, fallbackKind) };
}

function normalizeSourcedTextList(value: unknown, fallbackKind: ArxivSourceKind): ArxivSourcedText[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    const text = normalizeSourcedText(item, fallbackKind);
    return text ? [text] : [];
  });
}

function entryFromResult(value: unknown): Array<Record<string, any>> {
  if (!Array.isArray(value)) return [];
  return value.filter(isRecord);
}

function paperFromEntry(entry: Record<string, any>): Omit<ArxivPaperTrace, 'found_by' | 'first_seen_query' | 'duplicate_in' | 'status' | 'status_source_kind' | 'read_state' | 'read_coverage' | 'evidence' | 'invalid_evidence' | 'source_kind'> | null {
  const id = normalizeArxivId(entry.arxiv_id);
  if (!id) return null;
  return {
    arxiv_id: id,
    version: numberValue(entry.version),
    title: stringValue(entry.title),
    summary: stringValue(entry.summary),
    authors: stringList(entry.authors),
    categories: stringList(entry.categories),
    primary_category: stringValue(entry.primary_category) || undefined,
    published: stringValue(entry.published) || undefined,
    updated: stringValue(entry.updated) || undefined,
    abs_url: stringValue(entry.abs_url) || undefined,
    pdf_url: stringValue(entry.pdf_url) || undefined,
    downloaded: boolValue(entry.downloaded),
    paper_key: stringValue(entry.paper_key) || undefined,
  };
}

function traceFromReportQuery(row: Record<string, any>, fallbackIndex: number): ArxivQueryTrace | null {
  const callId = stringValue(row.tool_call_id) || `arxiv-report-query-${fallbackIndex + 1}`;
  const args = isRecord(row.args) ? { ...row.args } : {};
  const returnedIds = stringList(row.returned_ids).map(normalizeArxivId).filter(Boolean);
  return {
    tool_call_id: callId,
    query_number: numberValue(row.query_number) || fallbackIndex + 1,
    args,
    returned_count: numberValue(row.returned_count) ?? returnedIds.length,
    new_count: numberValue(row.new_count) ?? returnedIds.length,
    duplicate_count: numberValue(row.duplicate_count) ?? 0,
    upstream_total: numberValue(row.upstream_total),
    start: numberValue(row.start),
    items_per_page: numberValue(row.items_per_page),
    returned_ids: returnedIds,
    source_kind: 'deterministic',
    rationale: normalizeSourcedText(row.rationale, 'llm_judgment'),
    phase: normalizeSourcedText(row.phase, 'llm_judgment'),
    derived_from: Array.isArray(row.derived_from)
      ? row.derived_from.filter(isRecord).map((item) => ({
          arxiv_id: normalizeArxivId(item.arxiv_id),
          source_kind: item.source_kind === 'validated' ? 'validated' as const : 'invalid' as const,
        })).filter((item) => item.arxiv_id)
      : undefined,
  };
}

function screeningResult(envelopes: ToolResultEnvelope[]): Record<string, any> | null {
  for (let i = envelopes.length - 1; i >= 0; i -= 1) {
    const envelope = envelopes[i];
    if (!isArxivScreeningEnvelope(envelope) || !isRecord(envelope.result)) continue;
    return envelope.result;
  }
  return null;
}

export function buildArxivRunModel(input: ArxivAuditInput): ArxivRunModel | null {
  const envelopes = input.toolCalls
    .map((call) => getEnvelope(input.envelopesByCallId, call.id))
    .filter((envelope): envelope is ToolResultEnvelope => Boolean(envelope));
  const searchCalls = input.toolCalls.filter((call) => call.name === 'arxiv_search');
  const searchEnvelopes = searchCalls.map((call) => [call, getEnvelope(input.envelopesByCallId, call.id)] as const);
  const hasAnyArxiv = input.toolCalls.some((call) => call.name.startsWith('arxiv_')) || envelopes.some((envelope) => {
    const meta = envelope.result_metadata || {};
    return stringValue(meta.integration) === 'arxiv';
  });
  if (!hasAnyArxiv) return null;

  const seen = new Set<string>();
  const paperMap = new Map<string, ArxivPaperTrace>();
  const queries: ArxivQueryTrace[] = [];
  let returnedCount = 0;

  searchEnvelopes.forEach(([call, envelope], index) => {
    const result = isRecord(envelope?.result) ? envelope!.result : {};
    const entries = isArxivSearchEnvelope(envelope) ? entryFromResult(result.entries) : [];
    const returnedIds = entries.map((entry) => normalizeArxivId(entry.arxiv_id)).filter(Boolean);
    let newCount = 0;
    let duplicateCount = 0;
    returnedIds.forEach((id) => {
      if (seen.has(id)) duplicateCount += 1;
      else {
        seen.add(id);
        newCount += 1;
      }
    });
    returnedCount += returnedIds.length;
    const trace: ArxivQueryTrace = {
      tool_call_id: call.id,
      query_number: index + 1,
      args: { ...(call.args || {}) },
      returned_count: returnedIds.length,
      new_count: newCount,
      duplicate_count: duplicateCount,
      upstream_total: numberValue(result.total_results),
      start: numberValue(result.start),
      items_per_page: numberValue(result.items_per_page),
      returned_ids: returnedIds,
      source_kind: 'deterministic',
    };
    queries.push(trace);

    entries.forEach((entry) => {
      const paper = paperFromEntry(entry);
      if (!paper) return;
      const existing = paperMap.get(paper.arxiv_id);
      if (existing) {
        existing.found_by = Array.from(new Set([...existing.found_by, call.id]));
        existing.duplicate_in = Array.from(new Set([...existing.duplicate_in, call.id]));
        if (!existing.paper_key && paper.paper_key) existing.paper_key = paper.paper_key;
        existing.downloaded = Boolean(existing.downloaded || paper.downloaded);
        return;
      }
      paperMap.set(paper.arxiv_id, {
        ...paper,
        found_by: [call.id],
        first_seen_query: call.id,
        duplicate_in: [],
        status: 'not_screened',
        status_source_kind: 'deterministic',
        read_state: 'not_read',
        evidence: [],
        invalid_evidence: [],
        source_kind: 'deterministic',
      });
    });
  });

  envelopes
    .map(readCoverageFromEnvelope)
    .filter((item): item is ArxivReadCoverageEnvelope => Boolean(item))
    .forEach(({ arxiv_id, coverage, title, paper_key }) => {
      const existing = paperMap.get(arxiv_id);
      if (!existing) {
        paperMap.set(arxiv_id, {
          arxiv_id,
          title: title || `arXiv:${arxiv_id}`,
          summary: '',
          authors: [],
          categories: [],
          downloaded: true,
          paper_key: paper_key || coverage.paper_key,
          found_by: [],
          duplicate_in: [],
          status: 'not_screened',
          status_source_kind: 'deterministic',
          read_state: 'read',
          read_coverage: coverage,
          evidence: [],
          invalid_evidence: [],
          source_kind: 'deterministic',
        });
        return;
      }
      existing.read_state = 'read';
      existing.read_coverage = mergeReadCoverage(existing.read_coverage, coverage);
      existing.downloaded = true;
      if (!existing.paper_key && (paper_key || coverage.paper_key)) {
        existing.paper_key = paper_key || coverage.paper_key;
      }
    });

  const report = screeningResult(envelopes);
  const reportFunnel = isRecord(report?.funnel) ? report.funnel : null;
  const hasScreeningReport = Boolean(report);
  const validationWarnings: ArxivAuditWarning[] = [];
  const reportQueries = Array.isArray(report?.queries) ? report!.queries.filter(isRecord) : [];
  if (queries.length === 0 && reportQueries.length > 0) {
    reportQueries.forEach((row, index) => {
      const trace = traceFromReportQuery(row, index);
      if (!trace) return;
      queries.push(trace);
      returnedCount += trace.returned_count;
    });
  }
  reportQueries.forEach((row) => {
    const callId = stringValue(row.tool_call_id);
    const target = queries.find((query) => query.tool_call_id === callId);
    if (!target) return;
    target.rationale = normalizeSourcedText(row.rationale, 'llm_judgment');
    target.phase = normalizeSourcedText(row.phase, 'llm_judgment');
    if (Array.isArray(row.derived_from)) {
      target.derived_from = row.derived_from.filter(isRecord).map((item) => ({
        arxiv_id: normalizeArxivId(item.arxiv_id),
        source_kind: item.source_kind === 'validated' ? 'validated' as const : 'invalid' as const,
      })).filter((item) => item.arxiv_id);
    }
  });

  const reportPapers = Array.isArray(report?.papers) ? report!.papers.filter(isRecord) : [];
  reportPapers.forEach((row) => {
    const id = normalizeArxivId(row.arxiv_id);
    if (!id) return;
    let existing = paperMap.get(id);
    if (!existing) {
      const paper = paperFromEntry(row);
      if (!paper) return;
      if (boolValue(row.downloaded) === false) paper.paper_key = undefined;
      existing = {
        ...paper,
        found_by: stringList(row.found_by),
        first_seen_query: stringValue(row.first_seen_query) || undefined,
        duplicate_in: stringList(row.duplicate_in),
        status: 'not_screened',
        status_source_kind: 'deterministic',
        read_state: 'not_read',
        evidence: [],
        invalid_evidence: [],
        source_kind: 'deterministic',
      };
      paperMap.set(id, existing);
    }
    const status = stringValue(row.status) as ArxivPaperTrace['status'];
    if (['kept', 'possible', 'screened_out', 'not_screened'].includes(status)) {
      existing.status = status;
    }
    existing.status_source_kind = normalizeSourceKind(row.status_source_kind, 'llm_judgment');
    // Envelope-derived coverage is deterministic ground truth; a report row
    // without read_coverage must not erase it.
    const readCoverage = normalizeReadCoverage(row.read_coverage) || existing.read_coverage;
    existing.read_state =
      readCoverage || stringValue(row.read_state) === 'read' || existing.read_state === 'read'
        ? 'read'
        : 'not_read';
    existing.read_coverage = readCoverage;
    existing.reason = normalizeSourcedText(row.reason, 'llm_judgment');
    existing.evidence = normalizeEvidence(row.evidence);
    existing.invalid_evidence = normalizeWarning(row.invalid_evidence);
    if (boolValue(row.downloaded) === false) {
      existing.paper_key = undefined;
    }
  });

  const funnel: ArxivFunnel = {
    query_count: queries.length,
    returned_count: returnedCount,
    unique_count: paperMap.size,
  };
  if (reportFunnel) {
    const reportQueryCount = numberValue(reportFunnel.query_count);
    const reportReturnedCount = numberValue(reportFunnel.returned_count);
    const reportUniqueCount = numberValue(reportFunnel.unique_count);
    [
      ['query_count', funnel.query_count, reportQueryCount],
      ['returned_count', funnel.returned_count, reportReturnedCount],
      ['unique_count', funnel.unique_count, reportUniqueCount],
    ].forEach(([field, computed, reported]) => {
      if (typeof reported !== 'number' || computed === reported) return;
      validationWarnings.push({
        code: 'funnel_mismatch',
        message: `Screening report ${field}=${reported.toLocaleString()} differs from deterministic trace ${computed.toLocaleString()}.`,
      });
    });
    funnel.query_count = numberValue(reportFunnel.query_count) ?? funnel.query_count;
    funnel.returned_count = numberValue(reportFunnel.returned_count) ?? funnel.returned_count;
    funnel.unique_count = numberValue(reportFunnel.unique_count) ?? funnel.unique_count;
    funnel.read_paper_count = numberValue(reportFunnel.read_paper_count);
    funnel.agent_kept_count = numberValue(reportFunnel.agent_kept_count);
    funnel.agent_possible_count = numberValue(reportFunnel.agent_possible_count);
    funnel.agent_screened_out_count = numberValue(reportFunnel.agent_screened_out_count);
    funnel.not_screened_count = numberValue(reportFunnel.not_screened_count);
  }
  if (typeof funnel.read_paper_count !== 'number') {
    funnel.read_paper_count = Array.from(paperMap.values()).filter((paper) => paper.read_state === 'read').length;
  }

  return {
    schema: ARXIV_AUDIT_SCHEMA,
    status: input.status || 'complete',
    corpus: {
      provider: 'arxiv',
      boundary: stringValue(report?.corpus?.boundary) || 'arXiv public metadata and locally downloaded arXiv paper text only.',
      source_kind: 'static',
      untrusted_external_data: true,
    },
    funnel,
    queries,
    papers: Array.from(paperMap.values()),
    interpreted_request: normalizeSourcedText(report?.interpreted_request, 'llm_judgment'),
    concepts: normalizeSourcedTextList(report?.concepts, 'llm_judgment'),
    corpus_gaps: normalizeSourcedTextList(report?.corpus_gaps, 'llm_judgment'),
    validation_warnings: [...validationWarnings, ...normalizeWarning(report?.validation_warnings)],
    has_screening_report: hasScreeningReport,
  };
}

export function buildArxivRunModelFromEnvelope(toolCall: ToolCall, envelope?: ToolResultEnvelope): ArxivRunModel | null {
  return buildArxivRunModel({
    toolCalls: [toolCall],
    envelopesByCallId: toolCall.id ? { [toolCall.id]: envelope } : undefined,
    status: envelope ? 'complete' : 'live',
  });
}

export function buildArxivRunModelFromReport(report: unknown): ArxivRunModel | null {
  const callId = 'arxiv-screening-report';
  return buildArxivRunModel({
    toolCalls: [{ id: callId, name: 'arxiv_screening_report', args: {} }],
    envelopesByCallId: {
      [callId]: {
        artifact_id: callId,
        tool_name: 'arxiv_screening_report',
        tool_call_id: callId,
        ok: true,
        result_format: 'json',
        result: report,
        result_metadata: isRecord(report) && isRecord(report.result_metadata)
          ? report.result_metadata
          : { integration: 'arxiv', kind: 'screening_report' },
      },
    },
    status: 'complete',
  });
}

export function arxivAuditSummary(model: ArxivRunModel | null | undefined): string {
  if (!model) return '';
  const { funnel } = model;
  // No queries means this is not a search funnel (e.g. a pure reading group).
  // Return nothing so callers' purpose-built labels ("Reading N arXiv paper
  // windows", read locator summaries) win instead of a generic placeholder.
  if (funnel.query_count <= 0) return '';
  const verb = model.status === 'live' ? 'Searching arXiv' : 'Searched arXiv';
  let summary = `${verb} | ${funnel.query_count.toLocaleString()} ${funnel.query_count === 1 ? 'query' : 'queries'} | ${funnel.returned_count.toLocaleString()} returned -> ${funnel.unique_count.toLocaleString()} unique`;
  if (model.has_screening_report && typeof funnel.agent_kept_count === 'number') {
    const kept = funnel.agent_kept_count || 0;
    const possible = funnel.agent_possible_count || 0;
    const excluded = funnel.agent_screened_out_count || 0;
    const notScreened = funnel.not_screened_count || 0;
    const screened = kept + possible + excluded;
    if (notScreened > 0 && screened === 0) {
      summary += ' | screening incomplete';
    } else {
      summary += ` -> ${kept.toLocaleString()} agent-kept`;
      if (notScreened > 0) summary += ` | ${notScreened.toLocaleString()} unscreened`;
    }
  } else if (model.status === 'live') {
    summary += ' | screening pending';
  }
  return summary;
}

export function arxivQueryText(args: Record<string, unknown> | undefined): string {
  const source = args || {};
  const direct = stringValue(source.query) || stringValue(source.search_query) || stringValue(source.title);
  if (direct) return direct;
  const ids = stringList(source.ids);
  if (ids.length > 0) return `id:${ids.join(', ')}`;
  const categories = stringList(source.categories);
  const authors = stringList(source.authors);
  const parts = [
    categories.length > 0 ? `cat:${categories.join('|')}` : '',
    authors.length > 0 ? `au:${authors.join('|')}` : '',
  ].filter(Boolean);
  if (parts.length > 0) return parts.join(' ');
  return '';
}

export function arxivQueryOutcome(query: ArxivQueryTrace | null | undefined): ArxivQueryOutcome {
  if (!query) return 'pending';
  if (query.returned_count <= 0) return 'starved';
  if (query.new_count <= 0) {
    const start = Math.max(0, query.start ?? 0);
    const windowEnd = start + Math.max(0, query.returned_count);
    if (typeof query.upstream_total === 'number' && query.upstream_total > windowEnd) {
      return 'paginated';
    }
    return 'saturated';
  }
  return 'results';
}

export function arxivQueryOutcomeLabel(query: ArxivQueryTrace | null | undefined): string {
  if (!query) return 'Pending';
  switch (arxivQueryOutcome(query)) {
    case 'starved':
      return '0 results';
    case 'paginated':
      return `${plural(query.returned_count, 'result')} · 0 new — more upstream results available`;
    case 'saturated':
      return `${plural(query.returned_count, 'result')} · 0 new — coverage converging`;
    case 'results':
      return `${plural(query.returned_count, 'result')} · ${plural(query.new_count, 'new', 'new')}`;
    default:
      return 'Pending';
  }
}

export function arxivScreeningSummary(model: ArxivRunModel | null | undefined): string {
  if (!model || !model.has_screening_report) return '';
  const candidates = model.funnel.unique_count || model.papers.length;
  const kept = model.funnel.agent_kept_count ?? model.papers.filter((paper) => paper.status === 'kept').length;
  const possible = model.funnel.agent_possible_count ?? model.papers.filter((paper) => paper.status === 'possible').length;
  const excluded = model.funnel.agent_screened_out_count ?? model.papers.filter((paper) => paper.status === 'screened_out').length;
  const notScreened = model.funnel.not_screened_count ?? model.papers.filter((paper) => paper.status === 'not_screened').length;
  const screened = kept + possible + excluded;
  if (notScreened > 0 && screened === 0) {
    return `${plural(candidates, 'candidate')} collected · 0 screened`;
  }
  const keptPart = `${plural(kept, 'kept', 'kept')}`;
  const possiblePart = possible > 0 ? ` · ${plural(possible, 'possible', 'possible')}` : '';
  const prefix = notScreened > 0 && screened < candidates
    ? `Screened ${screened.toLocaleString()} of ${plural(candidates, 'candidate')}`
    : `Screened ${plural(candidates, 'candidate')}`;
  const unscreenedPart = notScreened > 0 ? ` · ${plural(notScreened, 'unscreened', 'unscreened')}` : '';
  return `${prefix} -> ${keptPart}${possiblePart} · ${plural(excluded, 'excluded', 'excluded')}${unscreenedPart}`;
}

export interface ArxivScreeningEvidenceStats {
  candidates: number;
  screened: number;
  kept: number;
  possible: number;
  excluded: number;
  not_screened: number;
  validated_spans: number;
  invalid_evidence: number;
  validation_warnings: number;
}

export function arxivScreeningEvidenceStats(model: ArxivRunModel | null | undefined): ArxivScreeningEvidenceStats {
  const empty: ArxivScreeningEvidenceStats = {
    candidates: 0,
    screened: 0,
    kept: 0,
    possible: 0,
    excluded: 0,
    not_screened: 0,
    validated_spans: 0,
    invalid_evidence: 0,
    validation_warnings: 0,
  };
  if (!model) return empty;
  const kept = model.funnel.agent_kept_count ?? model.papers.filter((paper) => paper.status === 'kept').length;
  const possible = model.funnel.agent_possible_count ?? model.papers.filter((paper) => paper.status === 'possible').length;
  const excluded = model.funnel.agent_screened_out_count ?? model.papers.filter((paper) => paper.status === 'screened_out').length;
  const notScreened = model.funnel.not_screened_count ?? model.papers.filter((paper) => paper.status === 'not_screened').length;
  return {
    candidates: model.funnel.unique_count || model.papers.length,
    screened: kept + possible + excluded,
    kept,
    possible,
    excluded,
    not_screened: notScreened,
    validated_spans: model.papers.reduce((count, paper) => count + paper.evidence.length, 0),
    invalid_evidence: model.papers.reduce((count, paper) => count + paper.invalid_evidence.length, 0),
    validation_warnings: model.validation_warnings.length,
  };
}

export function arxivScreeningEvidenceUnverified(model: ArxivRunModel | null | undefined): boolean {
  if (!model?.has_screening_report) return false;
  const stats = arxivScreeningEvidenceStats(model);
  return stats.screened > 0 && stats.validated_spans === 0;
}

/**
 * Join a read/download row to its paper trace in the group-scope run model.
 * IDs arrive in mixed shapes (bare ID, arXiv:prefix, abs/pdf URL, vN suffix);
 * both sides normalize through normalizeArxivId so the join is stable.
 */
export function findArxivPaperTrace(
  model: ArxivRunModel | null | undefined,
  idOrUrl: unknown,
): ArxivPaperTrace | null {
  if (!model) return null;
  const id = normalizeArxivId(idOrUrl);
  if (!id) return null;
  // Stored paper keys spell old-style IDs with "_" where arXiv uses "/".
  const keyAlias = id.replace(/_/g, '/');
  return (
    model.papers.find((paper) => paper.arxiv_id === id || paper.arxiv_id === keyAlias)
    || null
  );
}

export function arxivEnvelopeCached(envelope?: ToolResultEnvelope): boolean {
  if (!envelope) return false;
  const result = isRecord(envelope.result) ? envelope.result : {};
  const meta = isRecord(envelope.result_metadata) ? envelope.result_metadata : {};
  // already_existed is the deterministic re-download marker the Go tool
  // actually emits; cached/cache_hit are reserved generic markers.
  if (result.already_existed === true) return true;
  return result.cached === true || result.cache_hit === true || meta.cached === true || meta.cache_hit === true;
}

export function arxivPaperProvenanceLabel(paper: ArxivPaperTrace): string {
  const found = paper.found_by.length ? `Found by ${paper.found_by.length} ${paper.found_by.length === 1 ? 'query' : 'queries'}` : 'No query provenance';
  const status = paper.status === 'not_screened' ? 'Not screened' : `Agent-${paper.status.replace('_', '-')}`;
  const evidence = paper.evidence.length > 0 ? 'Evidence verified' : 'No verified evidence';
  const read = paper.read_state === 'read' ? 'Pages read' : 'Not read';
  return `${found} | ${status} | ${evidence} | ${read}`;
}
