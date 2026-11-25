import React from 'react';
import {
  AlertTriangle,
  ArrowLeft,
  BookOpen,
  CheckCircle2,
  ChevronDown,
  Download,
  ExternalLink,
  FileText,
  Loader2,
  Paperclip,
  Search,
  ShieldAlert,
} from 'lucide-react';
import { toast } from 'sonner';
import ArxivPaperViewer, { type ArxivTransientEvidence } from '@/components/arxiv/ArxivPaperViewer';
import MathText, { truncateMathText } from '@/components/arxiv/MathText';
import { safeArxivUrl, formatHighlightForChat } from '@/components/arxiv/arxivContentSafety';
import { useChatActions } from '@/components/chat/ChatActionsContext';
import {
  arxivAuditSummary,
  arxivReadDetailCountParts,
  arxivReadInsightFromSources,
  arxivReadRegisterLabels,
  arxivReadShowsFindings,
  arxivScreeningEvidenceStats,
  arxivScreeningEvidenceUnverified,
  arxivScreeningSummary,
  buildArxivRunModelFromReport,
  type ArxivAuditWarning,
  type ArxivEvidenceSpan,
  type ArxivPaperTrace,
  type ArxivReadCoverage,
  type ArxivReadInsight,
  type ArxivRunModel,
} from '@/components/chat/toolActivity/arxivAuditModel';
import ConnectionLogo from '@/components/chat/ConnectionLogo';
import { CONNECTION_META } from '@/lib/connectionsCatalog';
import { cn } from '@/lib/utils';
import {
  ArxivApiError,
  DESKTOP_AUTH_REQUIRED_MESSAGE,
  downloadArxivPaper,
  fetchArxivPreviewPdfBlob,
  fetchArxivPaperPdfFile,
  fetchArxivPaperTextFile,
  getArxivPaper,
  isDesktopAuthRequiredError,
  normalizeArxivPaper,
  preloadArxivPaperPdfBlob,
  preloadArxivPreviewPdfBlob,
  type ArxivPaper,
  type ArxivSearchEntry,
} from '@/services/arxivApi';
import { openExternalUrl } from '@/services/openExternal';
import { registerCustomResolver } from '../../registry';
import type { RendererProps } from '../../types';

type PaperSelection = {
  kind: 'stored';
  paperKey: string;
  initialMode?: 'text' | 'pdf';
  initialOffset?: number;
  transientEvidence?: ArxivTransientEvidence[];
} | {
  kind: 'preview';
  entry: SearchEntry;
};

type SearchEntry = ArxivSearchEntry & { raw?: unknown };

function isObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function pickString(raw: unknown, keys: string[]): string {
  if (!isObject(raw)) return '';
  for (const key of keys) {
    const value = raw[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function pickNumber(raw: unknown, keys: string[], fallback = 0): number {
  if (!isObject(raw)) return fallback;
  for (const key of keys) {
    const value = raw[key];
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim() && Number.isFinite(Number(value))) {
      return Number(value);
    }
  }
  return fallback;
}

function pickBool(raw: unknown, keys: string[]): boolean {
  if (!isObject(raw)) return false;
  return keys.some((key) => raw[key] === true);
}

function pickStringArray(raw: unknown, keys: string[]): string[] {
  if (!isObject(raw)) return [];
  for (const key of keys) {
    const value = raw[key];
    if (Array.isArray(value)) {
      return value
        .map((item) => (typeof item === 'string' ? item.trim() : ''))
        .filter(Boolean);
    }
  }
  return [];
}

function normalizeEntry(raw: unknown): SearchEntry {
  return {
    arxivId: pickString(raw, ['arxiv_id', 'arxivId']),
    paperKey: pickString(raw, ['paper_key', 'paperKey']) || undefined,
    version: pickNumber(raw, ['version']),
    title: pickString(raw, ['title']) || '(untitled)',
    summary: pickString(raw, ['summary', 'abstract']),
    authors: pickStringArray(raw, ['authors']),
    categories: pickStringArray(raw, ['categories']),
    primaryCategory: pickString(raw, ['primary_category', 'primaryCategory']),
    published: pickString(raw, ['published']) || null,
    updated: pickString(raw, ['updated']) || null,
    absUrl: pickString(raw, ['abs_url', 'absUrl']) || null,
    pdfUrl: pickString(raw, ['pdf_url', 'pdfUrl']) || null,
    downloaded: pickBool(raw, ['downloaded']) || Boolean(pickString(raw, ['paper_key', 'paperKey'])),
    raw,
  };
}

function previewPaperFromEntry(entry: SearchEntry): ArxivPaper {
  return {
    paperKey: '',
    arxivId: entry.arxivId,
    version: entry.version,
    title: entry.title,
    summary: entry.summary,
    authors: entry.authors,
    categories: entry.categories,
    primaryCategory: entry.primaryCategory,
    published: entry.published,
    updated: entry.updated,
    addedAt: null,
    pdfState: 'none',
    pdfSizeBytes: 0,
    extractState: 'none',
    extractQuality: '',
    textChars: 0,
    absUrl: entry.absUrl,
    pdfUrl: entry.pdfUrl,
    lastError: null,
    raw: entry.raw ?? entry,
  };
}

function unwrapResult(raw: unknown): Record<string, any> {
  return isObject(raw) ? raw : {};
}

function resultKind(meta: Record<string, any>): string {
  const resultMeta = isObject(meta.result_metadata) ? meta.result_metadata : {};
  return String(resultMeta.kind || meta.kind || '').trim();
}

function arxivResultFromArtifact(artifact: RendererProps['artifact'], toolResult: unknown): Record<string, any> {
  if (isObject(toolResult)) return toolResult;
  const raw = artifact.metadata?.raw_result;
  if (isObject(raw)) return raw;
  if (typeof artifact.content === 'string' && artifact.content.trim()) {
    try {
      const parsed = JSON.parse(artifact.content);
      if (isObject(parsed)) return parsed;
    } catch {
      return {};
    }
  }
  return {};
}

function errorMessage(err: unknown, fallback: string): string {
  // Backend desktop-auth guard (401 DESKTOP_TOKEN_REQUIRED): this window was
  // opened without the launcher's token, so tell the user how to recover.
  if (isDesktopAuthRequiredError(err)) {
    return DESKTOP_AUTH_REQUIRED_MESSAGE;
  }
  if (err instanceof ArxivApiError) {
    if (err.status === 429 || err.status === 503) {
      return 'arXiv is rate limiting requests. Wait a moment, then retry.';
    }
    return err.message || fallback;
  }
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

function formatDate(value: string | null): string {
  if (!value) return '';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

function authorsPreview(authors: string[]): string {
  if (authors.length === 0) return 'Unknown authors';
  if (authors.length <= 3) return authors.join(', ');
  return `${authors.slice(0, 3).join(', ')} +${authors.length - 3}`;
}

function Header({ title, subtitle }: { title: string; subtitle: string }) {
  const meta = CONNECTION_META.arxiv;
  return (
    <div className="flex flex-col gap-2 border-b border-border px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex h-[30px] w-[30px] shrink-0 items-center justify-center overflow-hidden rounded-md border border-border bg-muted/50">
          <ConnectionLogo meta={meta} className="h-4 w-4 object-contain" />
        </span>
        <span className="min-w-0 truncate font-mono text-[15px] text-foreground">{title}</span>
        <span className="inline-flex items-center gap-1 rounded-md border border-emerald-500/25 bg-emerald-500/10 px-2 py-0.5 text-[11px] font-medium text-emerald-500 dark:text-emerald-400">
          <CheckCircle2 size={11} aria-hidden />
          Success
        </span>
      </div>
      <div className="text-[11px] text-muted-foreground">{subtitle}</div>
    </div>
  );
}

function StatusBadge({ status }: { status: ArxivPaperTrace['status'] }) {
  const label = status === 'not_screened' ? 'Not screened' : status.replace('_', ' ');
  const cls =
    status === 'kept'
      ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-500 dark:text-emerald-400'
      : status === 'possible'
        ? 'border-amber-500/30 bg-amber-500/10 text-amber-500'
        : status === 'screened_out'
          ? 'border-muted-foreground/20 bg-muted/30 text-muted-foreground'
          : 'border-border bg-background text-muted-foreground';
  return (
    <span className={cn('inline-flex h-6 items-center rounded-md border px-2 text-[11px] font-medium capitalize', cls)}>
      {label}
    </span>
  );
}

function SourceKindBadge({ kind }: { kind: string }) {
  const label = kind.replace(/_/g, ' ');
  const cls = kind === 'validated'
    ? 'border-emerald-500/25 bg-emerald-500/10 text-emerald-500 dark:text-emerald-400'
    : kind === 'llm_judgment'
      ? 'border-amber-500/25 bg-amber-500/10 text-amber-500'
      : 'border-border bg-muted/30 text-muted-foreground';
  return (
    <span className={cn('inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-medium capitalize', cls)}>
      {label}
    </span>
  );
}

function readCoverageLabel(coverage?: ArxivReadCoverage): string {
  if (!coverage) return 'Not read';
  const ranges = coverage.page_ranges
    .map((range) => {
      if (typeof range.start_page === 'number' && typeof range.end_page === 'number') {
        return range.start_page === range.end_page
          ? `${range.start_page}`
          : `${range.start_page}-${range.end_page}`;
      }
      return '';
    })
    .filter(Boolean);
  const pageCount = typeof coverage.page_count === 'number' && coverage.page_count > 0
    ? ` of ${coverage.page_count.toLocaleString()}`
    : '';
  if (ranges.length > 0) return `Read pages ${ranges.join(', ')}${pageCount}`;
  if (typeof coverage.pages_read_count === 'number' && coverage.pages_read_count > 0) {
    return `Read ${coverage.pages_read_count.toLocaleString()} page${coverage.pages_read_count === 1 ? '' : 's'}${pageCount}`;
  }
  return 'Read pages';
}

function ReadCoverageBadge({
  coverage,
  status,
}: {
  coverage?: ArxivReadCoverage;
  status?: ArxivPaperTrace['status'] | string;
}) {
  const read = Boolean(coverage);
  const needsRead = status === 'kept' || status === 'possible';
  const cls = read
    ? 'border-emerald-500/25 bg-emerald-500/10 text-emerald-500 dark:text-emerald-400'
    : needsRead
      ? 'border-amber-500/25 bg-amber-500/10 text-amber-500'
      : 'border-border bg-muted/30 text-muted-foreground';
  return (
    <span className={cn('inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-medium', cls)}>
      {readCoverageLabel(coverage)}
    </span>
  );
}

function readCoverageFromRaw(raw: unknown): ArxivReadCoverage | undefined {
  if (!isObject(raw)) return undefined;
  const ranges = Array.isArray(raw.page_ranges)
    ? raw.page_ranges.filter(isObject).map((range) => ({
        tool_call_id: pickString(range, ['tool_call_id']),
        start_page: pickNumber(range, ['start_page']) || undefined,
        end_page: pickNumber(range, ['end_page']) || undefined,
        pages_read: pickNumber(range, ['pages_read']) || undefined,
        page_count: pickNumber(range, ['page_count']) || undefined,
        content_source_kind: pickString(range, ['content_source_kind']) || undefined,
      }))
    : [];
  return {
    source_kind: 'deterministic',
    read_state: 'read',
    read_tool_call_ids: pickStringArray(raw, ['read_tool_call_ids']),
    latest_tool_call_id: pickString(raw, ['latest_tool_call_id']) || undefined,
    page_ranges: ranges,
    pages_read_count: pickNumber(raw, ['pages_read_count']) || undefined,
    page_count: pickNumber(raw, ['page_count']) || undefined,
    paper_key: pickString(raw, ['paper_key']) || undefined,
    extract_state: pickString(raw, ['extract_state']) || undefined,
    extract_quality: pickString(raw, ['extract_quality']) || undefined,
    content_source_kind: pickString(raw, ['content_source_kind']) || undefined,
  };
}

function warningGroupKey(warning: ArxivAuditWarning): string {
  return [
    warning.code,
    warning.message,
    warning.source_field || '',
    warning.tool_call_id || '',
  ].join('\u0000');
}

function groupAuditWarnings(warnings: ArxivAuditWarning[]): Array<{
  key: string;
  warning: ArxivAuditWarning;
  count: number;
  affectedPaperCount: number;
}> {
  const groups = new Map<string, {
    warning: ArxivAuditWarning;
    count: number;
    arxivIds: Set<string>;
  }>();
  warnings.forEach((warning) => {
    const key = warningGroupKey(warning);
    const existing = groups.get(key);
    if (existing) {
      existing.count += 1;
      if (warning.arxiv_id) existing.arxivIds.add(warning.arxiv_id);
      return;
    }
    groups.set(key, {
      warning,
      count: 1,
      arxivIds: new Set(warning.arxiv_id ? [warning.arxiv_id] : []),
    });
  });
  return Array.from(groups.entries()).map(([key, group]) => ({
    key,
    warning: group.warning,
    count: group.count,
    affectedPaperCount: group.arxivIds.size,
  }));
}

function queryText(args: Record<string, unknown>): string {
  return pickString(args, ['query', 'search_query', 'title', 'id_list']) || JSON.stringify(args);
}

function evidenceToTransient(evidence: ArxivEvidenceSpan): ArxivTransientEvidence {
  return {
    sourceField: evidence.source_field,
    start: evidence.start_byte,
    end: evidence.end_byte,
    quote: evidence.quote,
    label: 'Validated evidence',
  };
}

function bibtexKey(paper: ArxivPaperTrace): string {
  const firstAuthor = (paper.authors[0] || 'arxiv').split(/\s+/).pop() || 'arxiv';
  const year = paper.published ? new Date(paper.published).getFullYear() : '';
  return `${firstAuthor}${year || ''}${paper.arxiv_id.replace(/[^a-z0-9]+/gi, '')}`;
}

function buildBibtex(papers: ArxivPaperTrace[]): string {
  return papers.map((paper) => {
    const fields = [
      `  title = {${paper.title || `arXiv:${paper.arxiv_id}`}}`,
      paper.authors.length ? `  author = {${paper.authors.join(' and ')}}` : '',
      paper.published ? `  year = {${new Date(paper.published).getFullYear()}}` : '',
      `  eprint = {${paper.arxiv_id}${paper.version ? `v${paper.version}` : ''}}`,
      paper.primary_category ? `  archivePrefix = {arXiv},\n  primaryClass = {${paper.primary_category}}` : '  archivePrefix = {arXiv}',
      paper.abs_url ? `  url = {${paper.abs_url}}` : '',
    ].filter(Boolean).join(',\n');
    return `@misc{${bibtexKey(paper)},\n${fields}\n}`;
  }).join('\n\n');
}

const FILL_GAPS_PROMPT = 'Broaden the arXiv search to fill coverage gaps. Reformulate zero-result queries, relax quoted phrases and field prefixes, replace slash-separated terms with alternatives, and report the updated search funnel before screening.';

function keepPaperPrompt(paper: ArxivPaperTrace): string {
  return `Keep arXiv:${paper.arxiv_id} in the working set despite the screening verdict. Treat this as human_judgment and explain how it changes the shortlist: ${paper.title || `arXiv:${paper.arxiv_id}`}`;
}

function ResearchAuditView({
  model,
  toolName,
  onOpenPaper,
  onOpenEvidence,
  onAttachPaper,
  onFillGaps,
  onKeepPaper,
  onPrimePaper,
  attachBusyKey,
  canAttach,
}: {
  model: ArxivRunModel;
  toolName: string;
  onOpenPaper: (paper: ArxivPaperTrace) => void;
  onOpenEvidence: (paper: ArxivPaperTrace, evidence: ArxivEvidenceSpan) => void;
  onAttachPaper: (paperKey: string) => void;
  onFillGaps?: () => void;
  onKeepPaper?: (paper: ArxivPaperTrace) => void;
  onPrimePaper?: (paper: ArxivPaperTrace) => void;
  attachBusyKey: string;
  canAttach: boolean;
}) {
  const overrideStorageKey = React.useMemo(() => {
    const ids = model.papers.map((paper) => paper.arxiv_id).sort().join('|');
    return ids ? `arxiv-audit-kept-overrides:${ids}` : '';
  }, [model.papers]);
  const [keptOverrides, setKeptOverrides] = React.useState<Set<string>>(() => new Set());
  React.useEffect(() => {
    if (!overrideStorageKey || typeof window === 'undefined') return;
    try {
      const raw = window.sessionStorage.getItem(overrideStorageKey);
      const parsed = raw ? JSON.parse(raw) : [];
      setKeptOverrides(new Set(Array.isArray(parsed) ? parsed.filter((item) => typeof item === 'string') : []));
    } catch {
      setKeptOverrides(new Set());
    }
  }, [overrideStorageKey]);
  React.useEffect(() => {
    if (!overrideStorageKey || typeof window === 'undefined') return;
    try {
      window.sessionStorage.setItem(overrideStorageKey, JSON.stringify(Array.from(keptOverrides)));
    } catch {
      // Session persistence is an affordance only; keep the in-memory override.
    }
  }, [keptOverrides, overrideStorageKey]);
  const summary = arxivAuditSummary(model);
  const keptCount = model.papers.filter((paper) => paper.status === 'kept' || keptOverrides.has(paper.arxiv_id)).length;
  const possibleCount = model.papers.filter((paper) => paper.status === 'possible').length;
  const starvedQueryCount = model.queries.filter((query) => query.returned_count === 0).length;
  const thinCoverage = model.funnel.unique_count > 0 && model.funnel.unique_count < 8;
  const exportPapers = model.papers.filter((paper) => paper.status === 'kept' || paper.status === 'possible' || keptOverrides.has(paper.arxiv_id));
  const rawKeptCount = model.funnel.agent_kept_count ?? model.papers.filter((paper) => paper.status === 'kept').length;
  const rawPossibleCount = model.funnel.agent_possible_count ?? model.papers.filter((paper) => paper.status === 'possible').length;
  const rawExcludedCount = model.funnel.agent_screened_out_count ?? model.papers.filter((paper) => paper.status === 'screened_out').length;
  const rawNotScreenedCount = model.funnel.not_screened_count ?? model.papers.filter((paper) => paper.status === 'not_screened').length;
  const screeningSummary = arxivScreeningSummary(model);
  const screeningIncomplete = model.has_screening_report && rawNotScreenedCount > 0 && rawKeptCount + rawPossibleCount + rawExcludedCount === 0;
  const evidenceStats = arxivScreeningEvidenceStats(model);
  const evidenceUnverified = arxivScreeningEvidenceUnverified(model);
  const groupedWarnings = React.useMemo(
    () => groupAuditWarnings(model.validation_warnings),
    [model.validation_warnings],
  );

  return (
    <div className="flex h-full flex-col overflow-auto text-foreground">
      <Header title="arXiv research audit" subtitle={summary || toolName} />
      <div className="space-y-4 p-4">
        <section className="rounded-md border border-border bg-card/45 p-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-foreground">Discovery funnel</span>
            <span className="font-mono text-xs text-muted-foreground">
              {model.funnel.query_count.toLocaleString()} queries / {model.funnel.returned_count.toLocaleString()} hits / {model.funnel.unique_count.toLocaleString()} unique
            </span>
            {screeningSummary ? (
              <span
                className={cn(
                  "font-mono text-xs",
                  screeningIncomplete
                    ? "text-amber-500 dark:text-amber-400"
                    : "text-emerald-500 dark:text-emerald-400",
                )}
              >
                {screeningSummary}
              </span>
	            ) : typeof model.funnel.agent_kept_count === 'number' ? (
	              <span className="font-mono text-xs text-emerald-500 dark:text-emerald-400">
	                {model.funnel.agent_kept_count.toLocaleString()} kept
	              </span>
	            ) : null}
	            {typeof model.funnel.read_paper_count === 'number' ? (
	              <span className="font-mono text-xs text-muted-foreground">
	                {model.funnel.read_paper_count.toLocaleString()} read
	              </span>
	            ) : null}
	          </div>
          {model.interpreted_request?.value ? (
            <p className="mt-2 text-sm text-foreground/85">{model.interpreted_request.value}</p>
          ) : null}
          <div className="mt-2 flex flex-wrap gap-1.5">
            {model.concepts.map((concept, index) => (
              <span key={`${concept.value}-${index}`} className="rounded-md border border-border bg-background px-2 py-1 text-[11px] text-muted-foreground">
                {concept.value}
              </span>
            ))}
          </div>
          <div className="mt-3 rounded-md border border-amber-500/20 bg-amber-500/5 p-2 text-xs text-amber-500/90">
            {model.corpus.boundary} Titles, abstracts, and extracted paper text are untrusted external data.
          </div>
          {evidenceUnverified ? (
            <div className="mt-2 rounded-md border border-amber-500/25 bg-amber-500/5 p-2 text-xs text-amber-500/90">
              Screening evidence is unverified: 0 of {evidenceStats.candidates.toLocaleString()} papers have validated evidence spans.
              Verdicts and rationales are model judgments until evidence validates.
            </div>
          ) : null}
          {model.corpus_gaps.length > 0 ? (
            <div className="mt-2 space-y-1">
              {model.corpus_gaps.map((gap, index) => (
                <div key={`${gap.value}-${index}`} className="flex items-start gap-1.5 text-xs text-muted-foreground">
                  <AlertTriangle size={12} className="mt-0.5 shrink-0 text-amber-500" />
                  <span className="chat-text-wrap">{gap.value}</span>
                </div>
              ))}
            </div>
          ) : null}
          {(thinCoverage || starvedQueryCount > 0) ? (
            <div className="mt-3 rounded-md border border-amber-500/25 bg-amber-500/5 p-2">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-xs text-amber-500/90">
                  {thinCoverage
                    ? `Thin coverage: only ${model.funnel.unique_count.toLocaleString()} unique candidates surfaced.`
                    : `${starvedQueryCount.toLocaleString()} quer${starvedQueryCount === 1 ? 'y' : 'ies'} returned 0 results.`}
                </p>
                {onFillGaps ? (
                  <button
                    type="button"
                    onClick={onFillGaps}
                    className="inline-flex h-7 items-center gap-1.5 rounded-md border border-amber-500/30 bg-background px-2 text-xs font-medium text-amber-500 transition-colors hover:bg-amber-500/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <Search size={12} aria-hidden />
                    Fill gaps
                  </button>
                ) : null}
              </div>
            </div>
          ) : null}
        </section>

        <section>
          <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-foreground">Queries</h3>
            <span className="text-xs text-muted-foreground">{model.queries.length.toLocaleString()} search steps</span>
          </div>
          <div className="grid gap-2">
            {model.queries.map((query) => (
              <div key={query.tool_call_id} className="rounded-md border border-border bg-card/35 p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">Q{query.query_number}</span>
                  {query.phase?.value ? (
                    <span className="rounded border border-border bg-background px-1.5 py-0.5 text-[10px] font-medium uppercase text-muted-foreground">
                      {query.phase.value}
                    </span>
                  ) : null}
                  <span className="font-mono text-xs text-foreground">{queryText(query.args)}</span>
                </div>
                <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                  <span>{query.returned_count.toLocaleString()} returned</span>
                  <span>{query.new_count.toLocaleString()} new</span>
                  <span>{query.duplicate_count.toLocaleString()} duplicate</span>
                  {typeof query.upstream_total === 'number' ? <span>{query.upstream_total.toLocaleString()} upstream</span> : null}
                  {query.derived_from?.length ? <span>seeded by {query.derived_from.map((item) => `arXiv:${item.arxiv_id}`).join(', ')}</span> : null}
                </div>
                {query.rationale?.value ? (
                  <p className="mt-2 text-xs leading-5 text-foreground/80">{query.rationale.value}</p>
                ) : null}
              </div>
            ))}
          </div>
        </section>

        <section>
          <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-foreground">Working Set</h3>
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs text-muted-foreground">{keptCount.toLocaleString()} kept / {possibleCount.toLocaleString()} possible</span>
              {exportPapers.length > 0 ? (
                <button
                  type="button"
                  onClick={() => {
                    void navigator.clipboard.writeText(buildBibtex(exportPapers));
                    toast.success('Copied BibTeX for the working set.');
                  }}
                  className="rounded-md border border-border bg-background px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  Export BibTeX
                </button>
              ) : null}
            </div>
          </div>
          <div className="grid gap-3">
            {model.papers.map((paper) => {
              const paperKey = paper.downloaded !== false ? paper.paper_key || '' : '';
              const absUrl = safeArxivUrl(paper.abs_url || null);
              const manuallyKept = keptOverrides.has(paper.arxiv_id);
              return (
                <div
                  key={paper.arxiv_id}
                  onPointerEnter={() => onPrimePaper?.(paper)}
                  className="rounded-md border border-border bg-card/45 p-3"
                >
                  <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0 flex-1">
                      <button
                        type="button"
                        onClick={() => onOpenPaper(paper)}
                        onFocus={() => onPrimePaper?.(paper)}
                        className="block break-words text-left text-sm font-semibold leading-snug text-foreground transition-colors hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <MathText text={paper.title || `arXiv:${paper.arxiv_id}`} inlineOnly />
                      </button>
                      <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                        <span className="font-mono">arXiv:{paper.arxiv_id}{paper.version ? `v${paper.version}` : ''}</span>
	                        {paper.primary_category ? <span>{paper.primary_category}</span> : null}
	                        {paper.found_by.length ? <span>{paper.found_by.length} {paper.found_by.length === 1 ? 'query' : 'queries'}</span> : null}
	                        {paper.evidence.length > 0 ? <span className="text-emerald-500 dark:text-emerald-400">{paper.evidence.length} verified span{paper.evidence.length === 1 ? '' : 's'}</span> : null}
	                        <ReadCoverageBadge coverage={paper.read_coverage} status={manuallyKept ? 'kept' : paper.status} />
	                      </div>
                    </div>
                    <div className="flex shrink-0 flex-wrap justify-end gap-1.5">
                      <StatusBadge status={manuallyKept ? 'kept' : paper.status} />
                      {paperKey ? <SmallButton icon={BookOpen} onClick={() => onOpenPaper(paper)} variant="primary">Open</SmallButton> : null}
                      {paperKey && canAttach ? (
                        <SmallButton icon={attachBusyKey === paperKey ? Loader2 : Paperclip} iconClassName={attachBusyKey === paperKey ? 'animate-spin' : undefined} disabled={attachBusyKey === paperKey} onClick={() => onAttachPaper(paperKey)}>
                          {attachBusyKey === paperKey ? 'Attaching' : 'Attach'}
                        </SmallButton>
                      ) : null}
                      {!paperKey && absUrl ? <ExternalArxivLink href={absUrl} /> : null}
                    </div>
                  </div>
                  {paper.reason?.value ? (
                    <div className="mt-2 rounded-md border border-border/70 bg-background/45 p-2 text-xs leading-5 text-foreground/80">
                      <div className="mb-1 flex items-center gap-1.5">
                        <span className="text-[10px] uppercase text-muted-foreground">Agent rationale</span>
                        <SourceKindBadge kind={paper.reason.source_kind} />
                      </div>
                      {paper.reason.value}
                    </div>
                  ) : null}
                  {paper.summary ? (
                    <p className="mt-2 break-words text-xs leading-5 text-muted-foreground">
                      <MathText text={truncateMathText(paper.summary, 360)} inlineOnly />
                    </p>
                  ) : null}
                  {paper.evidence.length > 0 ? (
                    <div className="mt-2 space-y-1.5">
                      {paper.evidence.map((evidence, index) => (
                        <button
                          key={`${paper.arxiv_id}-${evidence.source_field}-${evidence.start_byte}-${index}`}
                          type="button"
                          onClick={() => onOpenEvidence(paper, evidence)}
                          className="block w-full rounded-md border border-orange-500/25 bg-orange-500/10 p-2 text-left transition-colors hover:bg-orange-500/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <div className="mb-1 flex flex-wrap items-center gap-1.5 text-[10px] uppercase text-orange-400">
                            <span>{evidence.source_field.replace('_', ' ')}</span>
                            <SourceKindBadge kind={evidence.source_kind} />
                          </div>
                          <blockquote className="chat-text-wrap text-xs leading-5 text-foreground/85">
                            <MathText text={truncateMathText(evidence.quote, 260)} inlineOnly />
                          </blockquote>
                        </button>
                      ))}
                    </div>
                  ) : null}
                  {paper.invalid_evidence.length > 0 ? (
                    <div className="mt-2 space-y-1">
                      {paper.invalid_evidence.map((warning, index) => (
                        <div key={`${warning.code}-${index}`} className="flex items-start gap-1.5 text-xs text-amber-500/90">
                          <AlertTriangle size={12} className="mt-0.5 shrink-0" />
                          <span className="chat-text-wrap">{warning.message || warning.code}</span>
                        </div>
                      ))}
                    </div>
                  ) : null}
                  {paper.status === 'screened_out' && !manuallyKept ? (
                    <div className="mt-2">
                      <button
                        type="button"
                        onClick={() => {
                          setKeptOverrides((prev) => new Set(prev).add(paper.arxiv_id));
                          onKeepPaper?.(paper);
                        }}
                        className="rounded-md border border-border bg-background px-2.5 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        Keep anyway
                      </button>
                    </div>
                  ) : manuallyKept ? (
                    <div className="mt-2 text-xs text-emerald-500 dark:text-emerald-400">Kept for this review session.</div>
                  ) : null}
                </div>
              );
            })}
          </div>
        </section>

        {groupedWarnings.length > 0 ? (
          <section className="rounded-md border border-amber-500/20 bg-amber-500/5 p-3">
            <h3 className="text-sm font-semibold text-amber-500">Validation Warnings</h3>
            <div className="mt-2 space-y-1">
              {groupedWarnings.map(({ key, warning, count, affectedPaperCount }) => (
                <div key={key} className="flex flex-wrap items-start gap-x-2 gap-y-1 text-xs text-amber-500/90">
                  <AlertTriangle size={12} className="mt-0.5 shrink-0" />
                  <span className="chat-text-wrap">{warning.message || warning.code}</span>
                  {count > 1 ? (
                    <span className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 font-mono text-[10px]">
                      x{count.toLocaleString()}
                    </span>
                  ) : null}
                  {affectedPaperCount > 1 ? (
                    <span className="text-[11px] text-amber-500/70">
                      {affectedPaperCount.toLocaleString()} papers
                    </span>
                  ) : warning.arxiv_id ? (
                    <span className="font-mono text-[11px] text-amber-500/70">arXiv:{warning.arxiv_id}</span>
                  ) : null}
                </div>
              ))}
            </div>
          </section>
        ) : null}
      </div>
    </div>
  );
}

function sourcedValue(raw: unknown): string {
  if (typeof raw === 'string') return raw.trim();
  if (isObject(raw)) return pickString(raw, ['value']);
  return '';
}

function sourcedKind(raw: unknown): string {
  if (!isObject(raw)) return '';
  return pickString(raw, ['source_kind', 'sourceKind']);
}

function sourcedListValues(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  return raw.map(sourcedValue).filter(Boolean);
}

function RecommendationSetView({
  result,
  toolName,
  className,
}: {
  result: Record<string, any>;
  toolName: string;
  className?: string;
}) {
  const rows = Array.isArray(result.recommendations)
    ? result.recommendations.filter(isObject)
    : [];
  const report = isObject(result.screening_report) ? result.screening_report : {};
  const reportCallId = pickString(report, ['tool_call_id', 'toolCallId'])
    || pickString(result.result_metadata, ['screening_report_tool_call_id']);
  const reportArtifactId = pickString(report, ['artifact_id', 'artifactId'])
    || pickString(result.result_metadata, ['screening_report_artifact_id']);
  const selectionBasis = sourcedValue(result.selection_basis);
  const warnings = Array.isArray(result.validation_warnings)
    ? result.validation_warnings.filter(isObject) as ArxivAuditWarning[]
    : [];
  const groupedWarnings = groupAuditWarnings(warnings);
  const metadataResolution = pickString(result, ['metadata_resolution'])
    || 'Resolve full paper metadata and evidence text from the linked screening report.';
  const count = pickNumber(result, ['recommendation_count'], rows.length);

  return (
    <div className={cn('flex h-full flex-col overflow-auto text-foreground', className)}>
      <Header title="arXiv recommendations" subtitle={`${count.toLocaleString()} recommendation${count === 1 ? '' : 's'} · ${toolName}`} />
      <div className="space-y-4 p-4">
        <section className="rounded-md border border-border bg-card/45 p-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-foreground">Recommendation Set</span>
            {reportCallId ? (
              <span className="rounded border border-border bg-background px-2 py-1 font-mono text-[11px] text-muted-foreground">
                report {reportCallId}
              </span>
            ) : null}
            {reportArtifactId ? (
              <span className="rounded border border-border bg-background px-2 py-1 font-mono text-[11px] text-muted-foreground">
                artifact {reportArtifactId}
              </span>
            ) : null}
          </div>
          {selectionBasis ? <p className="mt-2 text-sm text-foreground/85">{selectionBasis}</p> : null}
          <div className="mt-3 rounded-md border border-amber-500/20 bg-amber-500/5 p-2 text-xs text-amber-500/90">
            {metadataResolution} Paper metadata remains untrusted external data.
          </div>
        </section>

        <section className="grid gap-3">
          {rows.length === 0 ? (
            <div className="rounded-md border border-border bg-card/45 p-4 text-sm text-muted-foreground">
              No valid recommendations were emitted.
            </div>
          ) : rows.map((row, index) => {
            const arxivId = pickString(row, ['arxiv_id', 'arxivId']);
            const version = pickNumber(row, ['version']);
            const rank = pickNumber(row, ['rank'], index + 1);
            const tier = pickString(row, ['tier']);
            const status = pickString(row, ['screening_status', 'screeningStatus']) as ArxivPaperTrace['status'];
            const reason = sourcedValue(row.reason);
            const reasonKind = sourcedKind(row.reason);
            const useFor = sourcedValue(row.use_for);
            const useForKind = sourcedKind(row.use_for);
            const caveats = sourcedListValues(row.caveats);
            const refs = Array.isArray(row.evidence_refs) ? row.evidence_refs.filter(isObject) : [];
            const readCoverage = readCoverageFromRaw(row.read_coverage);
            const absUrl = safeArxivUrl(arxivId ? `https://arxiv.org/abs/${arxivId}` : null);

            return (
              <article key={`${arxivId || 'recommendation'}-${index}`} className="rounded-md border border-border bg-card/45 p-3">
                <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">#{rank}</span>
                      {tier ? (
                        <span className="rounded border border-border bg-background px-1.5 py-0.5 text-[10px] font-medium uppercase text-muted-foreground">
                          {tier}
                        </span>
                      ) : null}
                      {status ? <StatusBadge status={status} /> : null}
                    </div>
	                    <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
	                      {arxivId ? <span className="font-mono text-foreground">arXiv:{arxivId}{version ? `v${version}` : ''}</span> : null}
	                      {typeof row.screening_report_paper_index === 'number' ? (
	                        <span>report row {(row.screening_report_paper_index + 1).toLocaleString()}</span>
	                      ) : null}
	                      <ReadCoverageBadge coverage={readCoverage} status={status} />
	                    </div>
                  </div>
                  <ExternalArxivLink href={absUrl} />
                </div>

                {reason ? (
                  <div className="mt-3 rounded-md border border-border/70 bg-background/45 p-2 text-xs leading-5 text-foreground/80">
                    <div className="mb-1 flex items-center gap-1.5">
                      <span className="text-[10px] uppercase text-muted-foreground">Why recommended</span>
                      {reasonKind ? <SourceKindBadge kind={reasonKind} /> : null}
                    </div>
                    {reason}
                  </div>
                ) : null}

                {useFor ? (
                  <div className="mt-2 rounded-md border border-border/70 bg-background/45 p-2 text-xs leading-5 text-foreground/80">
                    <div className="mb-1 flex items-center gap-1.5">
                      <span className="text-[10px] uppercase text-muted-foreground">Use for</span>
                      {useForKind ? <SourceKindBadge kind={useForKind} /> : null}
                    </div>
                    {useFor}
                  </div>
                ) : null}

                {refs.length > 0 ? (
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {refs.map((ref, refIndex) => {
                      const evidenceIndex = pickNumber(ref, ['evidence_index'], refIndex);
                      const sourceField = pickString(ref, ['source_field']);
                      return (
                        <span key={`${arxivId}-evidence-${refIndex}`} className="rounded border border-orange-500/25 bg-orange-500/10 px-2 py-1 text-[11px] text-orange-400">
                          evidence {evidenceIndex + 1}{sourceField ? ` · ${sourceField}` : ''}
                        </span>
                      );
                    })}
                  </div>
                ) : null}

                {caveats.length > 0 ? (
                  <div className="mt-2 space-y-1">
                    {caveats.map((caveat, caveatIndex) => (
                      <div key={`${arxivId}-caveat-${caveatIndex}`} className="flex items-start gap-1.5 text-xs text-amber-500/90">
                        <AlertTriangle size={12} className="mt-0.5 shrink-0" />
                        <span className="chat-text-wrap">{caveat}</span>
                      </div>
                    ))}
                  </div>
                ) : null}
              </article>
            );
          })}
        </section>

        {groupedWarnings.length > 0 ? (
          <section className="rounded-md border border-amber-500/20 bg-amber-500/5 p-3">
            <h3 className="text-sm font-semibold text-amber-500">Validation Warnings</h3>
            <div className="mt-2 space-y-1">
              {groupedWarnings.map(({ key, warning, count, affectedPaperCount }) => (
                <div key={key} className="flex flex-wrap items-start gap-x-2 gap-y-1 text-xs text-amber-500/90">
                  <AlertTriangle size={12} className="mt-0.5 shrink-0" aria-hidden />
                  <span className="chat-text-wrap">{pickString(warning, ['message']) || pickString(warning, ['code'])}</span>
                  {count > 1 ? (
                    <span className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 font-mono text-[10px]">
                      x{count.toLocaleString()}
                    </span>
                  ) : null}
                  {affectedPaperCount > 1 ? (
                    <span className="text-[11px] text-amber-500/70">
                      {affectedPaperCount.toLocaleString()} papers
                    </span>
                  ) : warning.arxiv_id ? (
                    <span className="font-mono text-[11px] text-amber-500/70">arXiv:{warning.arxiv_id}</span>
                  ) : null}
                </div>
              ))}
            </div>
          </section>
        ) : null}
      </div>
    </div>
  );
}

function SmallButton({
  children,
  icon: Icon,
  iconClassName,
  onClick,
  disabled,
  variant = 'secondary',
}: {
  children: React.ReactNode;
  icon: React.ComponentType<{ size?: number; className?: string }>;
  iconClassName?: string;
  onClick: () => void;
  disabled?: boolean;
  variant?: 'primary' | 'secondary';
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={(e) => {
        e.stopPropagation();
        onClick();
      }}
      className={cn(
        'inline-flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 disabled:cursor-not-allowed disabled:opacity-60',
        variant === 'primary'
          ? 'border-primary/40 bg-primary/10 text-primary hover:bg-primary/15'
          : 'border-border bg-muted/30 text-foreground hover:bg-muted/60',
      )}
    >
      <Icon size={13} className={iconClassName} aria-hidden />
      {children}
    </button>
  );
}

function ExternalArxivLink({ href }: { href: string | null }) {
  if (!href) return null;
  return (
    <button
      type="button"
      aria-label="Open abstract page on arxiv.org"
      onClick={(e) => {
        e.stopPropagation();
        void openExternalUrl(href);
      }}
      className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-background px-2.5 text-xs text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
    >
      <ExternalLink size={13} aria-hidden />
      arXiv
    </button>
  );
}

function PaperRow({
  entry,
  paper,
  busy,
  attachBusy,
  onOpen,
  onDownload,
  onAttach,
  onPrimePdf,
}: {
  entry: SearchEntry;
  paper?: ArxivPaper;
  busy?: boolean;
  attachBusy?: boolean;
  onOpen: () => void;
  onDownload: () => void;
  onAttach?: () => void;
  onPrimePdf?: () => void;
}) {
  const paperKey = paper?.paperKey || entry.paperKey || '';
  const absUrl = safeArxivUrl(entry.absUrl);
  const date = formatDate(entry.published);
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={entry.title}
      onPointerEnter={onPrimePdf}
      onFocus={onPrimePdf}
      onClick={onOpen}
      onKeyDown={(e) => {
        // Enter/Space on the nested action buttons must trigger those buttons,
        // not the row's open action.
        if (e.target !== e.currentTarget) return;
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onOpen();
        }
      }}
      className="rounded-md border border-border bg-card/45 p-3 transition-colors hover:bg-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
    >
      <div className="flex min-w-0 flex-col gap-2">
        <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <h3 className="break-words text-sm font-semibold leading-snug text-foreground">
              <MathText text={entry.title} inlineOnly />
            </h3>
            <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
              {entry.arxivId ? <span className="font-mono">arXiv:{entry.arxivId}{entry.version ? `v${entry.version}` : ''}</span> : null}
              {entry.primaryCategory ? <span>{entry.primaryCategory}</span> : null}
              {date ? <span>{date}</span> : null}
              {paperKey ? <span className="text-emerald-500 dark:text-emerald-400">Downloaded</span> : null}
            </div>
          </div>
          <div className="flex shrink-0 flex-wrap justify-end gap-1.5">
            {paperKey ? (
              <SmallButton icon={BookOpen} onClick={onOpen} variant="primary">Open</SmallButton>
            ) : (
              <SmallButton icon={busy ? Loader2 : Download} iconClassName={busy ? 'animate-spin' : undefined} onClick={onDownload} disabled={busy} variant="primary">
                {busy ? 'Saving' : 'Download'}
              </SmallButton>
            )}
            {paperKey && onAttach ? (
              <SmallButton icon={attachBusy ? Loader2 : Paperclip} iconClassName={attachBusy ? 'animate-spin' : undefined} onClick={onAttach} disabled={attachBusy}>
                {attachBusy ? 'Attaching' : 'Attach'}
              </SmallButton>
            ) : null}
            <ExternalArxivLink href={absUrl} />
          </div>
        </div>
        <div className="text-xs text-muted-foreground">{authorsPreview(entry.authors)}</div>
        {entry.summary ? (
          <p className="break-words text-xs leading-5 text-foreground/80">
            <MathText text={truncateMathText(entry.summary, 460)} inlineOnly />
          </p>
        ) : null}
      </div>
    </div>
  );
}

function StatusView({ result, kind, toolName }: { result: Record<string, any>; kind: string; toolName: string }) {
  const status = isObject(result.status) ? result.status : result;
  const keys = ['enabled', 'paper_count', 'storage_bytes', 'contact_email_set', 'last_check_ok'];
  return (
    <div className="flex h-full flex-col overflow-auto text-foreground">
      <Header title={toolName} subtitle={kind === 'connection_status' ? 'arXiv connection check' : 'arXiv local library status'} />
      <div className="grid gap-2 p-4 sm:grid-cols-2">
        {keys.map((key) => (
          status[key] !== undefined ? (
            <div key={key} className="rounded-md border border-border bg-card/45 p-3">
              <div className="text-[11px] uppercase tracking-wide text-muted-foreground">{key.replace(/_/g, ' ')}</div>
              <div className="mt-1 font-mono text-sm text-foreground">{String(status[key])}</div>
            </div>
          ) : null
        ))}
      </div>
    </div>
  );
}

/* ------------------------------------------------------------------ */
/* Read detail view (L3) — identity → strip → insight → two expanders  */
/* ------------------------------------------------------------------ */

type ReadDetailSelection = {
  paperKey: string;
  initialMode?: 'text' | 'pdf';
  initialOffset?: number;
  transientEvidence?: ArxivTransientEvidence[];
};

/** Split text into plain/highlighted segments by exact quote matches. Quotes
    are validator-located verbatim strings; this only re-finds them for
    display and never alters the raw payload. */
function annotateQuoteSegments(text: string, quotes: string[]): Array<{ text: string; hl: boolean }> {
  if (!text || quotes.length === 0) return [{ text, hl: false }];
  const segments: Array<{ text: string; hl: boolean }> = [];
  let rest = text;
  while (rest.length > 0) {
    let best: { index: number; quote: string } | null = null;
    for (const quote of quotes) {
      if (!quote) continue;
      const index = rest.indexOf(quote);
      if (index >= 0 && (!best || index < best.index)) best = { index, quote };
    }
    if (!best) {
      segments.push({ text: rest, hl: false });
      break;
    }
    if (best.index > 0) segments.push({ text: rest.slice(0, best.index), hl: false });
    segments.push({ text: best.quote, hl: true });
    rest = rest.slice(best.index + best.quote.length);
  }
  return segments;
}

function AnnotatedExtract({ text, quotes }: { text: string; quotes: string[] }) {
  const segments = React.useMemo(() => annotateQuoteSegments(text, quotes), [text, quotes]);
  return (
    <>
      {segments.map((segment, index) =>
        segment.hl ? (
          <mark
            key={index}
            className="rounded bg-orange-500/20 px-0.5 text-inherit shadow-[inset_0_-1px_0_rgba(249,115,22,0.45)]"
          >
            {segment.text}
          </mark>
        ) : (
          <React.Fragment key={index}>{segment.text}</React.Fragment>
        ),
      )}
    </>
  );
}

function ReadExpanderButton({
  label,
  detail,
  open,
  onToggle,
}: {
  label: string;
  detail: string;
  open: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-expanded={open}
      className="flex w-full items-center gap-2 rounded-md border border-border px-3 py-2 text-left text-sm text-muted-foreground transition-colors hover:border-muted-foreground/40 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <ChevronDown size={14} aria-hidden className={`shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
      <span className="shrink-0 font-medium text-foreground/85">{label}</span>
      {detail ? <span className="min-w-0 truncate text-xs text-muted-foreground/75">— {detail}</span> : null}
    </button>
  );
}

function ReadPositionStrip({
  kind,
  startPage,
  endPage,
  pageCount,
  pagesExtracted,
  offset,
  windowChars,
  totalChars,
  method,
}: {
  kind: 'pages' | 'text';
  startPage: number;
  endPage: number;
  pageCount: number;
  pagesExtracted: number;
  offset: number;
  windowChars: number;
  totalChars: number;
  method: string;
}) {
  const total = kind === 'pages' ? pageCount : totalChars;
  const from = kind === 'pages' ? Math.max(0, startPage - 1) : Math.max(0, offset);
  const to = kind === 'pages' ? endPage : Math.min(total || offset + windowChars, offset + windowChars);
  const hasBar = total > 0 && to > from;
  const caption = kind === 'pages'
    ? [
        startPage > 0 && endPage >= startPage
          ? `pp. ${startPage.toLocaleString()}–${endPage.toLocaleString()}${pageCount > 0 ? ` of ${pageCount.toLocaleString()}` : ''}`
          : 'paper pages',
        pagesExtracted > 0 ? `${pagesExtracted.toLocaleString()} page${pagesExtracted === 1 ? '' : 's'} extracted` : '',
        method,
      ].filter(Boolean).join(' · ')
    : [
        `chars ${offset.toLocaleString()}–${(offset + windowChars).toLocaleString()}${totalChars > 0 ? ` of ${totalChars.toLocaleString()}` : ''}`,
        method,
      ].filter(Boolean).join(' · ');
  return (
    <div>
      {hasBar ? (
        <div className="relative h-1.5 w-full overflow-hidden rounded-full bg-muted" aria-hidden="true">
          <div
            className="absolute inset-y-0 rounded-full bg-orange-500/60"
            style={{
              left: `${Math.min(100, (from / total) * 100)}%`,
              width: `${Math.max(1.5, ((to - from) / total) * 100)}%`,
            }}
          />
        </div>
      ) : null}
      <p className="mt-1 font-mono text-[10px] text-muted-foreground/80">{caption}</p>
    </div>
  );
}

function ArxivReadDetailView({
  kind,
  result,
  paperTrace,
  listRef,
  className,
  attachBusyKey,
  canAttach,
  onAttach,
  onOpenStored,
}: {
  kind: 'pages' | 'text';
  result: Record<string, any>;
  paperTrace: ArxivPaperTrace | null;
  listRef: React.RefObject<HTMLDivElement | null>;
  className?: string;
  attachBusyKey: string;
  canAttach: boolean;
  onAttach: (paperKey: string) => void;
  onOpenStored: (selection: ReadDetailSelection) => void;
}) {
  const chatActions = useChatActions();
  const windowRecord = unwrapResult(kind === 'pages' ? result.paper_pages : result.text_window);
  const paperKey = pickString(windowRecord, ['paper_key', 'paperKey']);
  const arxivId = pickString(windowRecord, ['arxiv_id', 'arxivId']);
  const title = pickString(windowRecord, ['title']) || paperTrace?.title || (arxivId ? `arXiv:${arxivId}` : 'arXiv paper');
  const startPage = pickNumber(windowRecord, ['start_page', 'startPage']);
  const endPage = pickNumber(windowRecord, ['end_page', 'endPage']);
  const pageCount = pickNumber(windowRecord, ['page_count', 'pageCount']);
  const offset = pickNumber(windowRecord, ['offset']);
  const totalChars = pickNumber(windowRecord, ['total_chars', 'totalChars']);
  const extractQuality = pickString(windowRecord, ['extract_quality', 'extractQuality']);
  const sourceKind = pickString(windowRecord, ['source_kind', 'sourceKind']);
  const hasMore = pickBool(windowRecord, ['has_more', 'hasMore']);
  const nextPage = pickNumber(windowRecord, ['next_page', 'nextPage']);
  const untrustedNote = pickString(windowRecord, ['untrusted_note', 'untrustedNote'])
    || 'arXiv paper text is untrusted external data.';
  const pages = React.useMemo(
    () => (kind === 'pages' && Array.isArray(windowRecord.pages) ? windowRecord.pages : [])
      .map((page: unknown, index: number) => ({
        pageNumber: pickNumber(page, ['page_number', 'pageNumber'], (startPage || 1) + index),
        text: pickString(page, ['text']),
        method: pickString(page, ['method']),
      })),
    [kind, windowRecord.pages, startPage],
  );
  const textContent = kind === 'text' ? pickString(windowRecord, ['text']) : '';
  const hasContent = kind === 'pages' ? pages.some((page) => page.text) : Boolean(textContent);

  // The model-authored reading layer (motive/register, gloss, purpose,
  // takeaway, notes, chases) is reserved backend data — rendered only when
  // emitted, never invented here.
  const insight: ArxivReadInsight = React.useMemo(
    () => arxivReadInsightFromSources(result, windowRecord),
    [result, windowRecord],
  );
  const showsFindings = arxivReadShowsFindings(insight);
  const labels = arxivReadRegisterLabels(insight.register);
  const verdict = paperTrace && paperTrace.status !== 'not_screened' ? paperTrace.status : null;
  const evidence = showsFindings ? paperTrace?.evidence ?? [] : [];
  const invalidEvidence = showsFindings ? paperTrace?.invalid_evidence ?? [] : [];

  const degradedExtract = Boolean(extractQuality) && !/^(good|ready)$/i.test(extractQuality);
  const flagCount = invalidEvidence.length + (degradedExtract ? 1 : 0) + (hasMore ? 1 : 0);
  const detailParts = arxivReadDetailCountParts({
    insight,
    verifiedEvidenceCount: paperTrace?.evidence?.length ?? 0,
    flagCount,
  });
  const sentence = insight.takeaway
    ? { label: labels.insightLabel, text: insight.takeaway }
    : insight.purpose
      ? { label: labels.purposeLabel, text: insight.purpose }
      : showsFindings && paperTrace?.reason?.value
        ? { label: 'Verdict rationale (agent)', text: paperTrace.reason.value }
        : null;

  const annotatableQuotes = React.useMemo(() => {
    if (!showsFindings) return [];
    const quotes = (paperTrace?.evidence ?? [])
      .filter((span) => span.source_field === 'paper_text')
      .map((span) => span.quote)
      .filter(Boolean);
    const haystacks = kind === 'pages' ? pages.map((page) => page.text) : [textContent];
    return quotes.filter((quote) => haystacks.some((text) => text.includes(quote)));
  }, [showsFindings, paperTrace, kind, pages, textContent]);
  const [open, setOpen] = React.useState({ details: false, text: false });
  const [textMode, setTextMode] = React.useState<'annotated' | 'raw'>('annotated');
  const [queuedChases, setQueuedChases] = React.useState<Set<string>>(() => new Set());

  const purposeTag = insight.purpose_tag
    || (insight.register === 'notes' && insight.register_explicit ? 'SCAN' : '');
  const locator = `${arxivId ? `arXiv:${arxivId}` : 'arXiv paper'} · ${
    kind === 'pages'
      ? startPage > 0 && endPage >= startPage
        ? `pp. ${startPage}–${endPage}${pageCount > 0 ? ` / ${pageCount}` : ''}`
        : 'paper pages'
      : `chars from ${offset.toLocaleString()}${totalChars > 0 ? ` / ${totalChars.toLocaleString()}` : ''}`
  }`;
  const extractMethod = (pages[0]?.method || sourceKind).replace(/_/g, ' ');

  const queueChase = (chase: ArxivReadInsight['chase_candidates'][number]) => {
    const target = chase.arxiv_id ? `arXiv:${chase.arxiv_id}` : chase.ref;
    const prompt = `Queue an arXiv search for ${target}${chase.why ? ` — ${chase.why}` : ''}. Lineage: this query is seeded by the read of arXiv:${arxivId || paperTrace?.arxiv_id || 'this paper'}; record it as derived_from in the audit trail.`;
    const markQueued = () => setQueuedChases((prev) => new Set(prev).add(chase.ref));
    if (chatActions.insertDraftText) {
      chatActions.insertDraftText(prompt);
      toast.success('Added the chase query to the chat composer.');
      markQueued();
      return;
    }
    void navigator.clipboard.writeText(prompt)
      .then(() => {
        toast.success('Copied the chase query.');
        markQueued();
      })
      .catch(() => toast.error('Could not copy the chase query.'));
  };

  const detailLabel = detailParts.join(' · ')
    || (showsFindings ? 'read facts only' : 'scan facts only');
  const textLabel = kind === 'pages'
    ? [`${pages.length.toLocaleString()} page${pages.length === 1 ? '' : 's'}`, extractMethod, annotatableQuotes.length > 0 ? 'annotated' : 'raw'].filter(Boolean).join(' · ')
    : [`${textContent.length.toLocaleString()} chars`, annotatableQuotes.length > 0 ? 'annotated' : 'raw'].filter(Boolean).join(' · ');

  return (
    <div ref={listRef} tabIndex={-1} className={cn('flex h-full flex-col overflow-auto outline-none text-foreground', className)}>
      <Header title={kind === 'pages' ? 'arXiv paper read' : 'arXiv paper text read'} subtitle={locator} />
      <div className="flex flex-col gap-3 p-4">
        {/* 1 · identity — what the paper is */}
        <section className="rounded-md border border-border bg-card/45 p-3">
          <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
            <h3 className="min-w-0 flex-1 break-words text-sm font-semibold leading-snug text-foreground">
              <MathText text={title} inlineOnly />
            </h3>
            <span
              className={cn(
                'inline-flex shrink-0 items-center gap-1 rounded-md border px-2 py-0.5 text-[11px] font-medium',
                hasContent
                  ? 'border-emerald-500/25 bg-emerald-500/10 text-emerald-500 dark:text-emerald-400'
                  : 'border-amber-500/25 bg-amber-500/10 text-amber-500',
              )}
            >
              {hasContent ? (
                <>
                  <CheckCircle2 size={11} aria-hidden /> read complete
                </>
              ) : (
                <>
                  <AlertTriangle size={11} aria-hidden /> {kind === 'pages' ? '0 pages extracted' : 'empty window'}
                </>
              )}
            </span>
          </div>
          {insight.paper_gloss ? (
            <p className="mt-1 break-words text-sm leading-relaxed text-foreground/80">
              <MathText text={insight.paper_gloss} inlineOnly />
            </p>
          ) : null}
          <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
            {arxivId ? <span className="font-mono">arXiv:{arxivId}</span> : null}
            {purposeTag ? (
              <span className="rounded border border-orange-500/30 bg-orange-500/10 px-1.5 py-0.5 font-mono text-[9px] font-medium uppercase tracking-wide text-orange-600 dark:text-orange-400">
                {purposeTag}
              </span>
            ) : null}
            {verdict ? (
              <span title="Agent screening judgment from this run's report">
                verdict: {verdict.replace(/_/g, ' ')}
              </span>
            ) : null}
            <span
              title={untrustedNote}
              className="inline-flex items-center gap-1 text-muted-foreground/75"
            >
              <ShieldAlert size={11} aria-hidden className="text-amber-500" />
              external source
            </span>
          </div>
        </section>

        {/* 2 · position — where this read sits in the paper */}
        <ReadPositionStrip
          kind={kind}
          startPage={startPage}
          endPage={endPage}
          pageCount={pageCount}
          pagesExtracted={pages.filter((page) => page.text).length}
          offset={offset}
          windowChars={textContent.length}
          totalChars={totalChars}
          method={extractMethod}
        />

        {/* 3 · insight — one register-aware model sentence */}
        {sentence ? (
          <section className="rounded-md border border-border bg-card/45 p-3">
            <p className="chat-text-wrap text-sm leading-relaxed text-foreground/90">
              <span className="font-medium text-muted-foreground">{sentence.label}: </span>
              <MathText text={sentence.text} inlineOnly />
            </p>
            <div className="mt-1.5 flex flex-wrap gap-1.5 text-[10px]">
              {evidence.length > 0 ? (
                <span className="inline-flex items-center gap-1 rounded border border-emerald-500/25 bg-emerald-500/10 px-1.5 py-0.5 text-emerald-600 dark:text-emerald-400">
                  <CheckCircle2 size={10} aria-hidden />
                  {evidence.length === 1 ? '1 span verified' : `${evidence.length} spans verified`}
                </span>
              ) : null}
              {showsFindings && insight.captured_span_count > 0 ? (
                <span className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 text-amber-500/90">
                  {insight.captured_span_count === 1
                    ? '1 span captured · unverified'
                    : `${insight.captured_span_count} spans captured · unverified`}
                </span>
              ) : null}
              {!showsFindings && insight.register_explicit ? (
                <span className="rounded border border-border/70 bg-background/40 px-1.5 py-0.5 text-muted-foreground">
                  {labels.registerNote}
                </span>
              ) : null}
            </div>
          </section>
        ) : null}

        {/* 4 · indexed expanders — counts are the scent, nothing is hidden */}
        <ReadExpanderButton
          label="Details"
          detail={detailLabel}
          open={open.details}
          onToggle={() => setOpen((prev) => ({ ...prev, details: !prev.details }))}
        />
        {open.details ? (
          <div className="space-y-3 rounded-md border border-border/70 bg-card/30 p-3">
            {insight.purpose ? (
              <p className="text-xs leading-5 text-foreground/85">
                <span className="font-mono text-[10px] uppercase tracking-widest text-muted-foreground">
                  {labels.purposeLabel} ·{' '}
                </span>
                <MathText text={insight.purpose} inlineOnly />
              </p>
            ) : null}

            {evidence.length > 0 ? (
              <div className="space-y-1.5">
                {evidence.map((span, index) => (
                  <button
                    key={`${span.source_field}-${span.start_byte}-${index}`}
                    type="button"
                    disabled={!paperKey}
                    onClick={() => {
                      if (!paperKey) return;
                      onOpenStored({
                        paperKey,
                        initialMode: 'text',
                        initialOffset: span.source_field === 'paper_text'
                          ? Math.max(0, span.start_byte - 1500)
                          : 0,
                        transientEvidence: [evidenceToTransient(span)],
                      });
                    }}
                    className="block w-full rounded-md border-l-2 border-orange-400/70 bg-background/40 px-2.5 py-1.5 text-left transition-colors hover:bg-orange-500/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default"
                  >
                    <blockquote className="chat-text-wrap text-xs italic leading-snug text-foreground/85">
                      “<MathText text={truncateMathText(span.quote, 260)} inlineOnly />”
                    </blockquote>
                    <span className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px]">
                      <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                        <CheckCircle2 size={10} aria-hidden /> span verified
                      </span>
                      <span className="text-muted-foreground/75">
                        from {span.source_field === 'paper_text' ? 'paper text' : span.source_field === 'summary' ? 'abstract' : span.source_field}
                      </span>
                      <span className="text-amber-500/80">verbatim · untrusted external text</span>
                    </span>
                  </button>
                ))}
              </div>
            ) : null}

            {insight.notes.length > 0 ? (
              <div className="space-y-1.5">
                <p className="font-mono text-[10px] uppercase tracking-widest text-muted-foreground">
                  {labels.notesHeading}
                </p>
                {insight.notes.map((note, index) => (
                  <div key={`${note.tag}-${index}`} className="flex items-start gap-2 rounded-md border border-border/70 bg-background/40 px-2.5 py-1.5">
                    {note.tag ? (
                      <span className="shrink-0 rounded border border-border px-1.5 py-0.5 font-mono text-[9px] uppercase text-muted-foreground">
                        {note.tag}
                      </span>
                    ) : null}
                    <p className="chat-text-wrap min-w-0 flex-1 text-xs leading-relaxed text-foreground/85">
                      <MathText text={note.text} inlineOnly />
                    </p>
                  </div>
                ))}
              </div>
            ) : null}

            {insight.chase_candidates.length > 0 ? (
              <div className="space-y-1.5">
                <p className="font-mono text-[10px] uppercase tracking-widest text-muted-foreground">
                  chase candidates
                </p>
                {insight.chase_candidates.map((chase) => (
                  <div key={chase.ref} className="flex flex-wrap items-center gap-2 rounded-md border border-border/70 bg-background/40 px-2.5 py-1.5">
                    <span className="shrink-0 font-mono text-xs text-muted-foreground">{chase.ref}</span>
                    <span className="min-w-0 flex-1 text-xs text-foreground/85">
                      {chase.arxiv_id ? <span className="font-mono">arXiv:{chase.arxiv_id}</span> : null}
                      {chase.arxiv_id && chase.why ? ' — ' : ''}
                      {chase.why ? <span className="text-muted-foreground">{chase.why}</span> : null}
                    </span>
                    {queuedChases.has(chase.ref) ? (
                      <span className="inline-flex items-center gap-1 rounded border border-emerald-500/25 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] text-emerald-600 dark:text-emerald-400">
                        <CheckCircle2 size={10} aria-hidden /> queued · seeded by this read
                      </span>
                    ) : (
                      <button
                        type="button"
                        onClick={() => queueChase(chase)}
                        className="rounded-md border border-orange-500/40 px-2 py-0.5 text-xs text-orange-600 transition-colors hover:bg-orange-500/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:text-orange-400"
                      >
                        Queue search
                      </button>
                    )}
                  </div>
                ))}
              </div>
            ) : null}

            <div className="flex flex-wrap gap-1.5 text-[10px]">
              {degradedExtract ? (
                <span className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 text-amber-500/90">
                  extract degraded: {extractQuality.replace(/_/g, ' ')}
                </span>
              ) : null}
              {hasMore ? (
                <span
                  className="rounded border border-border/70 bg-background/40 px-1.5 py-0.5 text-muted-foreground"
                  title="The window stopped before the end of the paper"
                >
                  window stopped early{nextPage > 0 ? ` · next p. ${nextPage}` : ''}
                </span>
              ) : null}
              <span className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 text-amber-500/90" title={untrustedNote}>
                untrusted paper text
              </span>
            </div>
            {invalidEvidence.length > 0 ? (
              <div className="space-y-1">
                {invalidEvidence.map((warning, index) => (
                  <div key={`${warning.code}-${index}`} className="flex items-start gap-1.5 text-xs text-amber-500/90">
                    <AlertTriangle size={12} className="mt-0.5 shrink-0" aria-hidden />
                    <span className="chat-text-wrap">{warning.message || warning.code}</span>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        ) : null}

        <ReadExpanderButton
          label="Paper text"
          detail={textLabel}
          open={open.text}
          onToggle={() => setOpen((prev) => ({ ...prev, text: !prev.text }))}
        />
        {open.text ? (
          <div className="space-y-2 rounded-md border border-border/70 bg-card/30 p-3">
            {annotatableQuotes.length > 0 ? (
              <div className="flex items-center justify-between gap-2">
                <p className="font-mono text-[10px] uppercase tracking-widest text-muted-foreground">paper text</p>
                <div className="flex overflow-hidden rounded-md border border-border text-xs">
                  {(['annotated', 'raw'] as const).map((mode) => (
                    <button
                      key={mode}
                      type="button"
                      onClick={() => setTextMode(mode)}
                      aria-pressed={textMode === mode}
                      className={cn(
                        'px-2 py-0.5 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                        textMode === mode
                          ? 'bg-orange-500/90 font-medium text-background'
                          : 'text-muted-foreground hover:bg-muted',
                      )}
                    >
                      {mode}
                    </button>
                  ))}
                </div>
              </div>
            ) : null}
            {kind === 'pages' ? (
              pages.length === 0 ? (
                <div className="rounded-md border border-border bg-card/45 p-4 text-sm text-muted-foreground">
                  No extracted pages were returned.
                </div>
              ) : (
                pages.map((page, index) => (
                  <section key={`${page.pageNumber}:${index}`} className="rounded-md border border-border bg-card/35">
                    <div className="flex flex-wrap items-center gap-2 border-b border-border/60 px-3 py-2 text-xs">
                      <span className="font-semibold text-foreground">Page {page.pageNumber.toLocaleString()}</span>
                      {page.method ? <span className="text-muted-foreground">{page.method}</span> : null}
                    </div>
                    <pre className="max-h-56 overflow-auto whitespace-pre-wrap p-3 text-xs leading-5 text-foreground [overflow-wrap:break-word]">
                      {textMode === 'annotated' && annotatableQuotes.length > 0 ? (
                        <AnnotatedExtract text={page.text || '(No extractable text on this page.)'} quotes={annotatableQuotes} />
                      ) : (
                        page.text || '(No extractable text on this page.)'
                      )}
                    </pre>
                  </section>
                ))
              )
            ) : (
              <pre className="max-h-[46vh] overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/30 p-3 text-xs leading-5 text-foreground [overflow-wrap:break-word]">
                {textMode === 'annotated' && annotatableQuotes.length > 0 ? (
                  <AnnotatedExtract text={textContent} quotes={annotatableQuotes} />
                ) : (
                  textContent
                )}
              </pre>
            )}
            {(textMode === 'raw' || annotatableQuotes.length === 0) ? (
              <p className="text-[10px] text-muted-foreground/70">
                raw extract, unmodified — exactly what entered the model's context
              </p>
            ) : null}
          </div>
        ) : null}

        <div className="flex flex-wrap gap-2 pt-1">
          {paperKey ? (
            <SmallButton
              icon={BookOpen}
              onClick={() => onOpenStored({
                paperKey,
                initialMode: 'text',
                initialOffset: kind === 'text' ? offset : undefined,
              })}
              variant="primary"
            >
              Open Viewer
            </SmallButton>
          ) : null}
          {paperKey && canAttach ? (
            <SmallButton
              icon={attachBusyKey === paperKey ? Loader2 : Paperclip}
              iconClassName={attachBusyKey === paperKey ? 'animate-spin' : undefined}
              disabled={attachBusyKey === paperKey}
              onClick={() => onAttach(paperKey)}
            >
              {attachBusyKey === paperKey ? 'Attaching' : 'Attach Paper'}
            </SmallButton>
          ) : null}
          {kind === 'text' && paperKey && chatActions.insertDraftText && textContent ? (
            <SmallButton
              icon={FileText}
              onClick={() => {
                chatActions.insertDraftText?.(formatHighlightForChat({ title, arxivId, quote: textContent }));
                toast.success('Added excerpt to the chat composer.');
              }}
            >
              Send Excerpt
            </SmallButton>
          ) : null}
        </div>
      </div>
    </div>
  );
}

const ArxivToolRenderer: React.FC<RendererProps> = ({ artifact, toolResult, className }) => {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  const toolName = String(meta.tool_name || artifact.title || 'arxiv');
  const kind = resultKind(meta);
  const result = React.useMemo(() => arxivResultFromArtifact(artifact, toolResult), [artifact, toolResult]);
  const chatActions = useChatActions();
  const [selected, setSelected] = React.useState<PaperSelection | null>(() => {
    if (kind === 'paper' || kind === 'downloaded_paper') {
      const paper = normalizeArxivPaper(isObject(result.paper) ? result.paper : result);
      return paper.paperKey ? { kind: 'stored', paperKey: paper.paperKey } : null;
    }
    return null;
  });
  const [downloadedById, setDownloadedById] = React.useState<Record<string, ArxivPaper>>({});
  const [busyId, setBusyId] = React.useState('');
  const [attachBusyKey, setAttachBusyKey] = React.useState('');
  const listRef = React.useRef<HTMLDivElement | null>(null);
  const returnedFromViewerRef = React.useRef(false);
  const selectedPreviewPaper = React.useMemo(
    () => selected?.kind === 'preview' ? previewPaperFromEntry(selected.entry) : undefined,
    [selected],
  );
  const selectedPreviewPdfLoader = React.useCallback(() => {
    if (selected?.kind !== 'preview') {
      return Promise.reject(new Error('No arXiv preview is selected.'));
    }
    return fetchArxivPreviewPdfBlob(selected.entry.arxivId, selected.entry.pdfUrl, { priority: 'interactive' });
  }, [selected]);

  const closeViewer = React.useCallback(() => {
    returnedFromViewerRef.current = true;
    setSelected(null);
  }, []);

  // When the in-place viewer unmounts (back action), focus would otherwise
  // fall to document.body, outside the aria-modal dialog. Move it onto the
  // list container instead.
  React.useEffect(() => {
    if (!selected && returnedFromViewerRef.current) {
      returnedFromViewerRef.current = false;
      listRef.current?.focus({ preventScroll: true });
    }
  }, [selected]);

  const attachPaper = React.useCallback(
    async (paperOrKey: ArxivPaper | string) => {
      if (!chatActions.attachFile) return;
      const key = typeof paperOrKey === 'string' ? paperOrKey : paperOrKey.paperKey;
      if (!key) return;
      setAttachBusyKey(key);
      try {
        const paper = typeof paperOrKey === 'string' ? await getArxivPaper(paperOrKey) : paperOrKey;
        const file = paper.pdfState === 'stored'
          ? await fetchArxivPaperPdfFile(paper)
          : await fetchArxivPaperTextFile(paper);
        await Promise.resolve(chatActions.attachFile(file));
        toast.success(`Attached ${file.name} to the chat composer.`);
      } catch (err) {
        toast.error(errorMessage(err, 'Failed to attach the paper.'));
      } finally {
        setAttachBusyKey('');
      }
    },
    [chatActions],
  );

  const savePaper = React.useCallback(async (entry: SearchEntry) => {
    if (!entry.arxivId) return;
    setBusyId(entry.arxivId);
    try {
      const downloaded = await downloadArxivPaper(entry.arxivId);
      setDownloadedById((prev) => ({ ...prev, [entry.arxivId]: downloaded.paper }));
      setSelected((current) => (
        current?.kind === 'preview' && current.entry.arxivId === entry.arxivId
          ? { kind: 'stored', paperKey: downloaded.paper.paperKey }
          : current
      ));
      toast.success(downloaded.alreadyExisted ? 'Paper already in library.' : 'Paper saved.');
    } catch (err) {
      toast.error(errorMessage(err, 'Failed to save paper.'));
    } finally {
      setBusyId('');
    }
  }, []);

  if (selected) {
    const canGoBack = kind === 'search_results' || kind === 'paper_list' || kind === 'paper_text' || kind === 'paper_pages' || kind === 'screening_report' || result.schema === 'arxiv_research_audit.v1';
    const isPreview = selected.kind === 'preview';
    return (
      <div
        className="h-full"
        onKeyDown={(e) => {
          // Consume the first Escape as "back to list" so it doesn't close
          // the whole inspector while the user is mid-read. InspectorOverlay
          // listens on window (bubble), so stopPropagation suffices.
          if (e.key === 'Escape' && canGoBack) {
            e.stopPropagation();
            closeViewer();
          }
        }}
      >
        {selected.kind === 'stored' ? (
          <ArxivPaperViewer
            paperKey={selected.paperKey}
            initialMode={selected.initialMode}
            initialOffset={selected.initialOffset}
            transientEvidence={selected.transientEvidence}
            onRequestClose={canGoBack ? closeViewer : undefined}
            className={cn('h-full overflow-auto', className)}
          />
        ) : (
          <ArxivPaperViewer
            previewPaper={selectedPreviewPaper}
            previewPdfLoader={selectedPreviewPdfLoader}
            initialMode="pdf"
            onRequestClose={canGoBack ? closeViewer : undefined}
            onSavePreview={() => void savePaper(selected.entry)}
            savePreviewBusy={isPreview && busyId === selected.entry.arxivId}
            className={cn('h-full overflow-auto', className)}
          />
        )}
      </div>
    );
  }

  if (kind === 'screening_report' || result.schema === 'arxiv_research_audit.v1') {
    const model = buildArxivRunModelFromReport(result);
    if (model) {
      return (
        <ResearchAuditView
          model={model}
          toolName={toolName}
          attachBusyKey={attachBusyKey}
          canAttach={Boolean(chatActions.attachFile)}
          onAttachPaper={(paperKey) => void attachPaper(paperKey)}
          onFillGaps={() => {
            if (chatActions.insertDraftText) {
              chatActions.insertDraftText(FILL_GAPS_PROMPT);
              toast.success('Added a broaden-search request to the chat composer.');
              return;
            }
            void navigator.clipboard.writeText(FILL_GAPS_PROMPT)
              .then(() => toast.success('Copied a broaden-search request.'))
              .catch(() => toast.error('Could not copy the broaden-search request.'));
          }}
          onKeepPaper={(paper) => {
            const prompt = keepPaperPrompt(paper);
            if (chatActions.insertDraftText) {
              chatActions.insertDraftText(prompt);
              toast.success('Added the keep-anyway correction to the chat composer.');
              return;
            }
            void navigator.clipboard.writeText(prompt)
              .then(() => toast.success('Copied the keep-anyway correction.'))
              .catch(() => toast.error('Could not copy the keep-anyway correction.'));
          }}
          onPrimePaper={(paper) => {
            if (paper.downloaded !== false && paper.paper_key) {
              preloadArxivPaperPdfBlob(paper.paper_key);
              return;
            }
            preloadArxivPreviewPdfBlob(paper.arxiv_id, paper.pdf_url);
          }}
          onOpenPaper={(paper) => {
            if (paper.downloaded !== false && paper.paper_key) {
              setSelected({ kind: 'stored', paperKey: paper.paper_key, initialMode: 'text' });
              return;
            }
            setSelected({ kind: 'preview', entry: normalizeEntry(paper) });
          }}
          onOpenEvidence={(paper, evidence) => {
            if (paper.downloaded === false || !paper.paper_key) {
              toast.info('Download the paper before opening evidence in context.');
              return;
            }
            setSelected({
              kind: 'stored',
              paperKey: paper.paper_key,
              initialMode: 'text',
              initialOffset: evidence.source_field === 'paper_text'
                ? Math.max(0, evidence.start_byte - 1500)
                : 0,
              transientEvidence: [evidenceToTransient(evidence)],
            });
          }}
        />
      );
    }
  }

  if (kind === 'recommendation_set' || result.schema === 'arxiv_recommendations.v1') {
    return <RecommendationSetView result={result} toolName={toolName} className={className} />;
  }

  if (kind === 'status' || kind === 'connection_status') {
    return <StatusView result={result} kind={kind} toolName={toolName} />;
  }

  if (kind === 'paper_text' || kind === 'paper_pages') {
    // The chat card stamps the joined screening trace (verdict, rationale,
    // validated evidence) onto the artifact it opens; without it the detail
    // view renders deterministic read facts only.
    const paperTrace = isObject(meta.arxiv_paper_trace)
      ? (meta.arxiv_paper_trace as ArxivPaperTrace)
      : null;
    return (
      <ArxivReadDetailView
        kind={kind === 'paper_pages' ? 'pages' : 'text'}
        result={result}
        paperTrace={paperTrace}
        listRef={listRef}
        className={className}
        attachBusyKey={attachBusyKey}
        canAttach={Boolean(chatActions.attachFile)}
        onAttach={(paperKey) => void attachPaper(paperKey)}
        onOpenStored={(selection) => setSelected({ kind: 'stored', ...selection })}
      />
    );
  }

  const papersRaw = Array.isArray(result.papers)
    ? result.papers
    : isObject(result.paper)
      ? [result.paper]
      : [];
  const entriesRaw = Array.isArray(result.entries) ? result.entries : [];

  if (kind === 'paper_list' || papersRaw.length > 0) {
    const papers = papersRaw.map(normalizeArxivPaper).filter((paper) => paper.paperKey);
    return (
      <div ref={listRef} tabIndex={-1} className={cn('flex h-full flex-col overflow-auto outline-none text-foreground', className)}>
        <Header title="arXiv paper library" subtitle={`${papers.length.toLocaleString()} stored paper${papers.length === 1 ? '' : 's'}`} />
        <div className="flex flex-col gap-3 p-4">
          {papers.length === 0 ? (
            <div className="rounded-md border border-border bg-card/45 p-4 text-sm text-muted-foreground">No stored papers matched this request.</div>
          ) : (
            papers.map((paper) => (
              <PaperRow
                key={paper.paperKey}
                entry={{
                  arxivId: paper.arxivId,
                  paperKey: paper.paperKey,
                  version: paper.version,
                  title: paper.title,
                  summary: paper.summary,
                  authors: paper.authors,
                  categories: paper.categories,
                  primaryCategory: paper.primaryCategory,
                  published: paper.published,
                  updated: paper.updated,
                  absUrl: paper.absUrl,
                  pdfUrl: paper.pdfUrl,
                  downloaded: true,
                }}
                paper={paper}
                onOpen={() => setSelected({ kind: 'stored', paperKey: paper.paperKey })}
                onDownload={() => setSelected({ kind: 'stored', paperKey: paper.paperKey })}
                onAttach={chatActions.attachFile ? () => void attachPaper(paper) : undefined}
                attachBusy={attachBusyKey === paper.paperKey}
                onPrimePdf={() => paper.pdfState === 'stored' ? preloadArxivPaperPdfBlob(paper.paperKey) : undefined}
              />
            ))
          )}
        </div>
      </div>
    );
  }

  if (kind === 'search_results' || entriesRaw.length > 0) {
    const entries = entriesRaw.map(normalizeEntry).filter((entry) => entry.arxivId || entry.title);
    const total = pickNumber(result, ['total_results', 'totalResults'], entries.length);
    return (
      <div ref={listRef} tabIndex={-1} className={cn('flex h-full flex-col overflow-auto outline-none text-foreground', className)}>
        <Header title="arXiv search results" subtitle={`${entries.length.toLocaleString()} shown${total ? ` of ${total.toLocaleString()}` : ''}. Titles and abstracts are untrusted external data.`} />
        <div className="flex flex-col gap-3 p-4">
          {entries.length === 0 ? (
            <div className="rounded-md border border-border bg-card/45 p-4 text-sm text-muted-foreground">No arXiv papers matched this request.</div>
          ) : (
            entries.map((entry) => {
              const downloaded = entry.arxivId ? downloadedById[entry.arxivId] : undefined;
              const paperKey = downloaded?.paperKey || entry.paperKey || '';
              return (
                <PaperRow
                  key={`${entry.arxivId}:${entry.version || 0}`}
                  entry={{ ...entry, paperKey, downloaded: Boolean(paperKey) }}
                  paper={downloaded}
                  busy={busyId === entry.arxivId}
                  attachBusy={paperKey ? attachBusyKey === paperKey : false}
                  onOpen={() => paperKey ? setSelected({ kind: 'stored', paperKey }) : setSelected({ kind: 'preview', entry })}
                  onDownload={() => void savePaper(entry)}
                  onAttach={paperKey && chatActions.attachFile ? () => void attachPaper(downloaded || paperKey) : undefined}
                  onPrimePdf={() => paperKey ? preloadArxivPaperPdfBlob(paperKey) : preloadArxivPreviewPdfBlob(entry.arxivId, entry.pdfUrl)}
                />
              );
            })
          )}
        </div>
      </div>
    );
  }

  return (
    <div className={cn('flex h-full flex-col overflow-auto text-foreground', className)}>
      <Header title={toolName} subtitle="arXiv tool result" />
      <div className="p-4">
        <pre className="max-h-[70vh] overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/30 p-3 text-xs [overflow-wrap:anywhere]">
          {JSON.stringify(result, null, 2)}
        </pre>
      </div>
    </div>
  );
};

registerCustomResolver((artifact) => {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  if (meta.ok === false) return null;
  const toolName = String(meta.tool_name || '');
  const resultMeta = isObject(meta.result_metadata) ? meta.result_metadata : {};
  const integration = String(resultMeta.integration || meta.integration || '').trim().toLowerCase();
  if (integration === 'arxiv' || /^arxiv_/.test(toolName)) {
    return {
      component: ArxivToolRenderer,
      label: 'arXiv',
      icon: Search,
      ownsChrome: true,
    };
  }
  return null;
});

export default ArxivToolRenderer;
