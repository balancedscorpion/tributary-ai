import React from 'react';
import { AlertTriangle, CheckCircle2, ExternalLink, ShieldCheck } from 'lucide-react';
import {
  resolveWidgetKey,
  WidgetForKey,
} from '@/components/renderers/tool/github/widgets';
import {
  JiraIssueCard,
  extractJiraIssueFromResult,
} from '@/components/renderers/tool/jira/IssueCard';
import {
  JiraWidgetForKey,
  resolveJiraWidgetKey,
} from '@/components/renderers/tool/jira/widgets';
import { formatBytes } from '@/lib/formatters';
import type { Artifact, ToolCall, ToolResultEnvelope } from '@/types';
import { isSandboxedHtmlToolName } from '@/types';
import {
  classifyGeneratedImageError,
  generatedImageAltText,
  getGeneratedImageMetadata,
  isImageGenerationToolName,
  type GeneratedImageErrorCategory,
} from '@/lib/generatedImageArtifacts';
import {
  arxivAuditSummary,
  arxivPaperProvenanceLabel,
  arxivQueryOutcomeLabel,
  arxivQueryText,
  arxivReadInsightFromSources,
  arxivReadRegisterLabels,
  arxivReadShowsFindings,
  arxivScreeningSummary,
  buildArxivRunModelFromEnvelope,
  type ArxivReadInsight,
} from './arxivAuditModel';

/**
 * Inline preview registry for the tool-activity disclosure cards.
 *
 * The inline preview is the SECONDARY affordance on a tool card: the explicit
 * chevron toggle opens it (card click opens the full audit overlay instead).
 * Tool-specific previews register a matcher here; resolveInlineToolPreview
 * falls back to a generic truncated result block so every tool has *some*
 * preview. This mirrors the artifact panel's renderer registry
 * (components/renderers/registry.ts) at chat scale.
 */

export interface ToolInlinePreviewProps {
  toolCall: ToolCall;
  envelope?: ToolResultEnvelope;
  artifact?: Artifact;
}

interface InlinePreviewEntry {
  match: (toolCall: ToolCall, envelope?: ToolResultEnvelope) => boolean;
  component: React.ComponentType<ToolInlinePreviewProps>;
}

const entries: InlinePreviewEntry[] = [];

export function registerInlineToolPreview(entry: InlinePreviewEntry): void {
  entries.push(entry);
}

export function resolveInlineToolPreview(
  toolCall: ToolCall,
  envelope?: ToolResultEnvelope,
): React.ComponentType<ToolInlinePreviewProps> {
  for (const entry of entries) {
    try {
      if (entry.match(toolCall, envelope)) return entry.component;
    } catch {
      // A broken matcher must never take down the chat transcript.
    }
  }
  return GenericInlinePreview;
}

/* ------------------------------------------------------------------ */
/* Shared helpers                                                      */
/* ------------------------------------------------------------------ */

function compactText(value: unknown, maxLength = 120): string {
  const text = String(value || '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.length > maxLength ? `${text.slice(0, maxLength - 1).trimEnd()}…` : text;
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim();
    if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  }
  return '';
}

function isPlainRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value ?? '');
  }
}

const GENERIC_PREVIEW_CHAR_LIMIT = 1200;

/* ------------------------------------------------------------------ */
/* Generic fallback preview — truncated mono result/args block         */
/* ------------------------------------------------------------------ */

const GenericInlinePreview: React.FC<ToolInlinePreviewProps> = ({ toolCall, envelope }) => {
  const text = React.useMemo(() => {
    if (envelope) {
      const raw =
        envelope.result_format === 'text'
          ? String(envelope.result ?? '')
          : safeStringify(envelope.result);
      const trimmed = raw.trim();
      if (trimmed) return trimmed;
    }
    const argsText =
      typeof toolCall.argsText === 'string' && toolCall.argsText.trim()
        ? toolCall.argsText
        : safeStringify(toolCall.args || {});
    return argsText.trim();
  }, [envelope, toolCall.args, toolCall.argsText]);

  if (!text) {
    return (
      <p className="text-[11px] text-muted-foreground/60">No result preview available.</p>
    );
  }

  const truncated = text.length > GENERIC_PREVIEW_CHAR_LIMIT;
  const shown = truncated ? `${text.slice(0, GENERIC_PREVIEW_CHAR_LIMIT).trimEnd()}…` : text;

  return (
    <div className="min-w-0">
      <pre className="chat-text-wrap max-h-48 overflow-y-auto whitespace-pre-wrap break-words rounded-sm bg-background/60 px-2 py-1.5 font-mono text-[11px] leading-snug text-muted-foreground">
        {shown}
      </pre>
      {truncated && (
        <p className="mt-1 text-[10px] text-muted-foreground/50">
          Preview truncated — open the full audit for the complete result.
        </p>
      )}
    </div>
  );
};

/* ------------------------------------------------------------------ */
/* Generated HTML artifact preview — concise summary, never raw JSON    */
/* ------------------------------------------------------------------ */

/**
 * create_interactive_artifact / create_interactive_data_artifact results are
 * a generated HTML payload — raw JSON (or worse, the HTML blob in the args)
 * is useless here, and the artifact card right below the dropdown already IS
 * the preview. Render a short metadata summary instead. Error details belong
 * in the full audit overlay, not the transcript dropdown.
 */
const SandboxedHtmlArtifactInlinePreview: React.FC<ToolInlinePreviewProps> = ({
  toolCall,
  envelope,
  artifact,
}) => {
  const args = isPlainRecord(toolCall.args) ? toolCall.args : {};
  const artifactMeta =
    artifact && isPlainRecord(artifact.metadata) ? artifact.metadata : {};
  const resultMeta =
    envelope && isPlainRecord(envelope.result_metadata)
      ? envelope.result_metadata
      : {};
  const result = envelope && isPlainRecord(envelope.result) ? envelope.result : {};

  const kindLabel =
    firstString(artifactMeta.artifact_kind, resultMeta.artifact_kind, resultMeta.artifact_type) ===
      'interactive_data_html' ||
    toolCall.name === 'create_interactive_data_artifact'
      ? 'Interactive data HTML'
      : 'Interactive HTML';
  const title = compactText(
    firstString(artifact?.title, result.title, resultMeta.title, args.title),
    90,
  );
  const declaredBytes = Number(
    firstString(artifactMeta.byte_size, resultMeta.byte_size),
  );
  const bytes =
    Number.isFinite(declaredBytes) && declaredBytes > 0
      ? declaredBytes
      : typeof args.html === 'string'
        ? args.html.length
        : 0;
  const sizeLabel = bytes > 0 ? formatBytes(bytes) : '';

  const rowCount = Number(
    firstString(artifactMeta.data_source_row_count, resultMeta.data_source_row_count),
  );
  const sourceColumns = Array.isArray(artifactMeta.data_source_columns)
    ? artifactMeta.data_source_columns.length
    : Array.isArray(resultMeta.data_source_columns)
      ? resultMeta.data_source_columns.length
      : 0;
  const sourceTitle = compactText(
    firstString(artifactMeta.data_source_title, resultMeta.data_source_title),
    60,
  );
  const dataLine = [
    Number.isFinite(rowCount) && rowCount > 0 ? `${rowCount.toLocaleString()} rows` : '',
    sourceColumns > 0 ? `${sourceColumns} cols` : '',
    sourceTitle,
  ]
    .filter(Boolean)
    .join(' · ');

  if (!envelope) {
    return (
      <div className="min-w-0 space-y-0.5 text-[11px] text-muted-foreground">
        <p>Generating {kindLabel.toLowerCase()}…</p>
        {title && <p className="chat-text-wrap text-muted-foreground/70">{title}</p>}
      </div>
    );
  }

  if (envelope.ok === false) {
    const errorText = compactText(
      firstString(
        typeof envelope.result === 'string' ? envelope.result : '',
        result.error,
        result.message,
        result.error_message,
      ),
      120,
    );
    return (
      <div className="min-w-0 space-y-0.5 text-[11px] text-muted-foreground">
        <p className="font-medium text-red-400">{kindLabel} failed</p>
        {title && <p className="chat-text-wrap text-muted-foreground/80">{title}</p>}
        {errorText && <p className="chat-text-wrap text-red-400/80">{errorText}</p>}
        <p className="text-muted-foreground/50">
          Open the full audit for the request, result, and raw diagnostics.
        </p>
      </div>
    );
  }

  return (
    <div className="min-w-0 space-y-0.5 text-[11px] text-muted-foreground">
      <p className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">
        <span className="font-medium">{kindLabel}</span>
        {sizeLabel && (
          <>
            <span className="text-muted-foreground/40">·</span>
            <span>{sizeLabel}</span>
          </>
        )}
        <span className="text-muted-foreground/40">·</span>
        <span className="inline-flex items-center gap-1 text-emerald-500/90">
          <ShieldCheck size={11} className="shrink-0" />
          Sandboxed, no network
        </span>
      </p>
      {title && <p className="chat-text-wrap text-muted-foreground/80">{title}</p>}
      {dataLine && <p className="chat-text-wrap text-muted-foreground/70">{dataLine}</p>}
      <p className="text-muted-foreground/50">
        Shown as the artifact card below — open it for the full view.
      </p>
    </div>
  );
};

registerInlineToolPreview({
  match: (toolCall, envelope) => {
    if (isSandboxedHtmlToolName(toolCall.name)) return true;
    const md =
      envelope && isPlainRecord(envelope.result_metadata)
        ? envelope.result_metadata
        : {};
    return (
      isSandboxedHtmlToolName(md.tool_name) ||
      String(md.artifact_kind || '') === 'interactive_html' ||
      String(md.artifact_kind || '') === 'interactive_data_html'
    );
  },
  component: SandboxedHtmlArtifactInlinePreview,
});

/* ------------------------------------------------------------------ */
/* Rich widget preview — reuses inspector widgets at card scale         */
/* ------------------------------------------------------------------ */

/**
 * Tools whose inspector family ships real widgets (GitHub today) preview as
 * the RENDERED widget by default — count line, real table, links — with the
 * raw JSON one explicit tab away. Approval widgets are excluded: the chat
 * approval card already renders that flow inline.
 */
const RenderedJsonTabs: React.FC<
  ToolInlinePreviewProps & { children: React.ReactNode }
> = ({ toolCall, envelope, children }) => {
  const [tab, setTab] = React.useState<'rendered' | 'json'>('rendered');
  return (
    <div className="min-w-0">
      <div className="mb-1.5 flex items-center gap-1">
        {(['rendered', 'json'] as const).map((value) => (
          <button
            key={value}
            type="button"
            onClick={() => setTab(value)}
            aria-pressed={tab === value}
            className={`rounded px-2 py-0.5 text-[11px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
              tab === value
                ? 'bg-muted text-foreground'
                : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'
            }`}
          >
            {value === 'rendered' ? 'Rendered' : 'JSON'}
          </button>
        ))}
      </div>
      {tab === 'rendered' ? (
        <div className="max-h-72 min-w-0 overflow-auto">{children}</div>
      ) : (
        <GenericInlinePreview toolCall={toolCall} envelope={envelope} />
      )}
    </div>
  );
};

const RichWidgetInlinePreview: React.FC<ToolInlinePreviewProps> = ({ toolCall, envelope }) => {
  const widgetMeta = React.useMemo(
    () => ({
      tool_name: toolCall.name,
      result_metadata: envelope?.result_metadata,
    }),
    [toolCall.name, envelope?.result_metadata],
  );
  const widgetKey = resolveWidgetKey(widgetMeta);
  return (
    <RenderedJsonTabs toolCall={toolCall} envelope={envelope}>
      <WidgetForKey widgetKey={widgetKey} raw={envelope?.result} meta={widgetMeta} />
    </RenderedJsonTabs>
  );
};

registerInlineToolPreview({
  match: (toolCall, envelope) => {
    if (!/^github_/.test(String(toolCall.name || ''))) return false;
    if (!envelope || envelope.ok === false) return false;
    return (
      resolveWidgetKey({
        tool_name: toolCall.name,
        result_metadata: envelope.result_metadata,
      }) !== 'approval'
    );
  },
  component: RichWidgetInlinePreview,
});

/* ------------------------------------------------------------------ */
/* Jira family preview — every Jira/Atlassian widget at card scale      */
/* ------------------------------------------------------------------ */

/**
 * All Jira/Atlassian tools preview as the RENDERED widget by default —
 * comment threads, attachment tables, user directories, write-result
 * summaries — with the raw JSON one explicit tab away, exactly like the
 * GitHub family above. Approval widgets are excluded: the chat approval card
 * already renders that flow inline with live approve/reject controls.
 */
const JiraWidgetInlinePreview: React.FC<ToolInlinePreviewProps> = ({ toolCall, envelope }) => {
  const widgetMeta = React.useMemo(
    () => ({
      tool_name: toolCall.name,
      tool_args: toolCall.args,
      inline: true,
      result_metadata: envelope?.result_metadata,
    }),
    [toolCall.name, toolCall.args, envelope?.result_metadata],
  );
  const widgetKey = resolveJiraWidgetKey(widgetMeta);
  return (
    <RenderedJsonTabs toolCall={toolCall} envelope={envelope}>
      <JiraWidgetForKey widgetKey={widgetKey} raw={envelope?.result} meta={widgetMeta} />
    </RenderedJsonTabs>
  );
};

registerInlineToolPreview({
  match: (toolCall, envelope) => {
    if (!/^(jira|atlassian)_/.test(String(toolCall.name || ''))) return false;
    if (!envelope || envelope.ok === false) return false;
    const widgetKey = resolveJiraWidgetKey({
      tool_name: toolCall.name,
      result_metadata: envelope.result_metadata,
    });
    if (widgetKey === 'generic' || widgetKey === 'approval') return false;
    // The issue widget needs an extractable issue to render anything.
    if (widgetKey === 'issue') return extractJiraIssueFromResult(envelope.result) !== null;
    return true;
  },
  component: JiraWidgetInlinePreview,
});

/* ------------------------------------------------------------------ */
/* Jira issue preview — the dedicated issue card at card scale          */
/* (kept as a fallback for legacy envelopes whose tool name is not      */
/* jira_* but whose result_metadata still marks a Jira issue payload)   */
/* ------------------------------------------------------------------ */

const JiraIssueInlinePreview: React.FC<ToolInlinePreviewProps> = ({ toolCall, envelope }) => {
  const issue = extractJiraIssueFromResult(envelope?.result);
  if (!issue) return <GenericInlinePreview toolCall={toolCall} envelope={envelope} />;
  return (
    <RenderedJsonTabs toolCall={toolCall} envelope={envelope}>
      <JiraIssueCard issue={issue} bordered className="bg-background/60" />
    </RenderedJsonTabs>
  );
};

registerInlineToolPreview({
  match: (toolCall, envelope) => {
    if (!envelope || envelope.ok === false) return false;
    const md = envelope.result_metadata || {};
    if (String(md.product) === 'jira' && String(md.widget) === 'issue') return true;
    return String(toolCall.name || '') === 'jira_get_issue';
  },
  component: JiraIssueInlinePreview,
});

/* ------------------------------------------------------------------ */
/* arXiv research audit preview — funnel, query intent, paper evidence */
/* ------------------------------------------------------------------ */

function isArxivTool(toolCall: ToolCall, envelope?: ToolResultEnvelope): boolean {
  if (/^arxiv_/.test(String(toolCall.name || ''))) return true;
  const meta = envelope && isPlainRecord(envelope.result_metadata) ? envelope.result_metadata : {};
  return String(meta.integration || '').toLowerCase() === 'arxiv';
}

/** Gloss + one register-aware model sentence, shown only when the backend
    actually emitted the reading layer. Scan reads speak in noticed/observed
    language and never get findings framing. */
const ArxivReadInsightLine: React.FC<{ insight: ArxivReadInsight }> = ({ insight }) => {
  const labels = arxivReadRegisterLabels(insight.register);
  const sentence = insight.takeaway
    ? { label: labels.insightLabel, text: insight.takeaway }
    : insight.purpose
      ? { label: labels.purposeLabel, text: insight.purpose }
      : null;
  if (!insight.paper_gloss && !sentence) return null;
  return (
    <div className="space-y-0.5">
      {insight.paper_gloss ? (
        <p className="chat-text-wrap text-muted-foreground/80">
          {compactText(insight.paper_gloss, 160)}
        </p>
      ) : null}
      {sentence ? (
        <p className="chat-text-wrap">
          <span className="font-medium text-foreground/75">{sentence.label}: </span>
          {compactText(sentence.text, 200)}
        </p>
      ) : null}
      {!arxivReadShowsFindings(insight) && insight.register_explicit ? (
        <p className="text-[10px] text-muted-foreground/60">observations, not conclusions</p>
      ) : null}
    </div>
  );
};

const ArxivAuditInlinePreview: React.FC<ToolInlinePreviewProps> = ({ toolCall, envelope }) => {
  const model = React.useMemo(
    () => buildArxivRunModelFromEnvelope(toolCall, envelope),
    [toolCall, envelope],
  );

  if (toolCall.name === 'arxiv_read_paper' && isPlainRecord(envelope?.result)) {
    const pageWindow = isPlainRecord(envelope.result.paper_pages) ? envelope.result.paper_pages : {};
    const pages = Array.isArray(pageWindow.pages) ? pageWindow.pages : [];
    const startPage = Number(pageWindow.start_page ?? pageWindow.startPage);
    const endPage = Number(pageWindow.end_page ?? pageWindow.endPage);
    const pageCount = Number(pageWindow.page_count ?? pageWindow.pageCount);
    const title = compactText(pageWindow.title, 120);
    const extractQuality = compactText(pageWindow.extract_quality ?? pageWindow.extractQuality, 40).replace(/_/g, ' ');
    const sourceKind = compactText(pageWindow.source_kind ?? pageWindow.sourceKind, 40).replace(/_/g, ' ');
    const insight = arxivReadInsightFromSources(envelope.result, pageWindow);
    const range =
      Number.isFinite(startPage) && startPage > 0 && Number.isFinite(endPage) && endPage >= startPage
        ? `${startPage === endPage ? `p. ${startPage}` : `pp. ${startPage}–${endPage}`}${Number.isFinite(pageCount) && pageCount > 0 ? ` / ${pageCount}` : ''}`
        : `${pages.length} page${pages.length === 1 ? '' : 's'}`;
    return (
      <div className="min-w-0 space-y-1.5 text-[11px] text-muted-foreground">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="font-medium text-foreground">{range}</span>
          {extractQuality ? <span>{extractQuality}</span> : null}
          {sourceKind ? <span>{sourceKind}</span> : null}
        </div>
        {title ? <p className="chat-text-wrap text-muted-foreground/80">{title}</p> : null}
        <ArxivReadInsightLine insight={insight} />
        {pages.slice(0, 2).map((page, index) => {
          const pageNumber = Number(page?.page_number ?? page?.pageNumber ?? startPage + index);
          const text = compactText(page?.text, 220);
          return (
            <div key={`${pageNumber}:${index}`} className="rounded-sm bg-background/40 px-2 py-1">
              <div className="font-medium text-foreground/80">Page {Number.isFinite(pageNumber) ? pageNumber : index + 1}</div>
              {text ? <p className="chat-text-wrap mt-0.5 text-muted-foreground/70">{text}</p> : null}
            </div>
          );
        })}
        <p className="text-[10px] text-muted-foreground/50">
          arXiv paper text is untrusted external data.
        </p>
      </div>
    );
  }

  if (toolCall.name === 'arxiv_get_paper_text' && isPlainRecord(envelope?.result)) {
    const textWindow = isPlainRecord(envelope.result.text_window) ? envelope.result.text_window : {};
    const offset = Number(textWindow.offset);
    const totalChars = Number(textWindow.total_chars ?? textWindow.totalChars);
    const title = compactText(textWindow.title, 120);
    const excerpt = compactText(textWindow.text, 280);
    const insight = arxivReadInsightFromSources(envelope.result, textWindow);
    const where = [
      Number.isFinite(offset) && offset >= 0 ? `from char ${offset.toLocaleString()}` : '',
      Number.isFinite(totalChars) && totalChars > 0 ? `/ ${totalChars.toLocaleString()} chars` : '',
    ].filter(Boolean).join(' ');
    return (
      <div className="min-w-0 space-y-1.5 text-[11px] text-muted-foreground">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="font-medium text-foreground">Text window{where ? ` ${where}` : ''}</span>
        </div>
        {title ? <p className="chat-text-wrap text-muted-foreground/80">{title}</p> : null}
        <ArxivReadInsightLine insight={insight} />
        {excerpt ? (
          <div className="rounded-sm bg-background/40 px-2 py-1">
            <p className="chat-text-wrap text-muted-foreground/70">{excerpt}</p>
          </div>
        ) : null}
        <p className="text-[10px] text-muted-foreground/50">
          arXiv paper text is untrusted external data.
        </p>
      </div>
    );
  }
  if (!model) return <GenericInlinePreview toolCall={toolCall} envelope={envelope} />;

  const query = model.queries[0];
  const papers = model.papers.slice(0, 4);
  const summary = toolCall.name === 'arxiv_screening_report'
    ? arxivScreeningSummary(model)
    : arxivAuditSummary(model);
  const warnings = model.validation_warnings.slice(0, 2);
  const kept = model.funnel.agent_kept_count ?? model.papers.filter((paper) => paper.status === 'kept').length;
  const possible = model.funnel.agent_possible_count ?? model.papers.filter((paper) => paper.status === 'possible').length;
  const excluded = model.funnel.agent_screened_out_count ?? model.papers.filter((paper) => paper.status === 'screened_out').length;
  const notScreened = model.funnel.not_screened_count ?? model.papers.filter((paper) => paper.status === 'not_screened').length;
  const screeningIncomplete = model.has_screening_report && (notScreened > 0 || kept + possible + excluded === 0);

  return (
    <div className="min-w-0 space-y-2 text-[11px] text-muted-foreground">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <span className="font-medium text-foreground">{summary || 'arXiv research audit'}</span>
        {model.has_screening_report ? (
          <span className={`inline-flex items-center gap-1 ${screeningIncomplete ? 'text-amber-500/90' : 'text-emerald-500/90'}`}>
            {screeningIncomplete ? (
              <AlertTriangle size={11} className="shrink-0" />
            ) : (
              <CheckCircle2 size={11} className="shrink-0" />
            )}
            {screeningIncomplete ? 'Incomplete' : 'Screened'}
          </span>
        ) : (
          <span className="text-muted-foreground/60">Screening pending</span>
        )}
      </div>

      {query ? (
        <div className="rounded-sm border border-border/60 bg-background/50 px-2 py-1.5">
          <div className="chat-text-wrap font-mono text-[11px] text-foreground/85">
            {compactText(arxivQueryText(query.args), 140) || `Query ${query.query_number}`}
          </div>
          <div className="mt-1 flex flex-wrap gap-x-2 gap-y-0.5 text-muted-foreground/70">
            <span>{arxivQueryOutcomeLabel(query)}</span>
            {query.duplicate_count > 0 ? <span>{query.duplicate_count.toLocaleString()} duplicate</span> : null}
          </div>
          {query.rationale?.value ? (
            <p className="chat-text-wrap mt-1 text-muted-foreground/80">
              {compactText(query.rationale.value, 180)}
            </p>
          ) : null}
        </div>
      ) : null}

      {papers.length > 0 ? (
        <div className="space-y-1">
          {papers.map((paper) => (
            <div key={paper.arxiv_id} className="min-w-0 rounded-sm bg-background/40 px-2 py-1">
              <div className="flex min-w-0 items-center gap-1.5">
                <span className="min-w-0 truncate text-foreground/85">{paper.title || `arXiv:${paper.arxiv_id}`}</span>
                {paper.evidence.length > 0 ? (
                  <span className="inline-flex shrink-0 items-center gap-1 text-[10px] text-emerald-600 dark:text-emerald-400">
                    <CheckCircle2 size={11} aria-hidden />
                    {paper.evidence.length === 1 ? '1 span verified' : `${paper.evidence.length} spans verified`}
                  </span>
                ) : null}
              </div>
              <div className="chat-text-wrap mt-0.5 text-[10px] text-muted-foreground/60">
                {arxivPaperProvenanceLabel(paper)}
              </div>
            </div>
          ))}
        </div>
      ) : null}

      {warnings.length > 0 ? (
        <div className="space-y-0.5">
          {warnings.map((warning, index) => (
            <div key={`${warning.code}-${index}`} className="flex min-w-0 items-start gap-1.5 text-amber-500/90">
              <AlertTriangle size={11} className="mt-0.5 shrink-0" />
              <span className="chat-text-wrap">{compactText(warning.message || warning.code, 160)}</span>
            </div>
          ))}
        </div>
      ) : null}

      <p className="text-[10px] text-muted-foreground/50">
        arXiv metadata and paper text are untrusted external data.
      </p>
    </div>
  );
};

registerInlineToolPreview({
  match: (toolCall, envelope) => isArxivTool(toolCall, envelope),
  component: ArxivAuditInlinePreview,
});

/* ------------------------------------------------------------------ */
/* Search preview — query + linked result list                          */
/* ------------------------------------------------------------------ */

function isSearchToolName(toolName: string): boolean {
  const name = String(toolName || '').trim().toLowerCase();
  // arXiv tools have a dedicated preview; never let the generic search
  // matcher claim them (today only registration order protects this).
  if (/^arxiv_/.test(name)) return false;
  return name === 'web_search' || name.includes('search') || name === 'jira_find';
}

function asObjectArray(value: unknown): Array<Record<string, any>> {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is Record<string, any> => isPlainRecord(item));
}

function addSearchSourcesFromArray(
  out: Array<{ title: string; url: string; snippet?: string }>,
  items: Array<Record<string, any>>,
) {
  for (const item of items) {
    const title = firstString(
      item.title,
      item.summary,
      item.name,
      item.key ? `${item.key}${item.summary ? `: ${item.summary}` : ''}` : '',
      item.url,
    );
    const url = firstString(item.url, item.href, item.link, item.webUrl, item.web_url);
    const snippet = compactText(firstString(item.snippet, item.excerpt, item.text, item.description), 180);
    if (!title && !url && !snippet) continue;
    out.push({ title: title || url || snippet, url, snippet });
  }
}

export function getSearchPreview(
  toolCall: ToolCall,
  envelope?: ToolResultEnvelope,
): {
  query: string;
  results: Array<{ title: string; url: string; snippet?: string }>;
  message?: string;
} | null {
  if (!isSearchToolName(toolCall.name)) return null;
  const result = envelope?.result;
  const resultObj = isPlainRecord(result) ? result : {};
  const args = toolCall.args || {};
  const query = firstString(
    resultObj.query,
    resultObj.jql,
    resultObj.cql,
    args.query,
    args.jql,
    (args as any).search,
  );

  const results: Array<{ title: string; url: string; snippet?: string }> = [];
  addSearchSourcesFromArray(results, asObjectArray(resultObj.results));
  addSearchSourcesFromArray(results, asObjectArray(resultObj.content));
  addSearchSourcesFromArray(results, asObjectArray(resultObj.issues));
  addSearchSourcesFromArray(results, asObjectArray(resultObj.pages));
  addSearchSourcesFromArray(results, asObjectArray(resultObj.candidates));
  addSearchSourcesFromArray(results, asObjectArray(resultObj.projects?.candidates));
  addSearchSourcesFromArray(results, asObjectArray(resultObj.boards?.candidates));
  addSearchSourcesFromArray(results, asObjectArray(resultObj.issues?.issues));

  const total = firstString(resultObj.total, resultObj.totalSize, resultObj.count);
  const fallbackMessage = total ? `${total} result${total === '1' ? '' : 's'} returned` : '';
  const message = compactText(firstString(resultObj.message, resultObj.summary, fallbackMessage), 220);
  const deduped: Array<{ title: string; url: string; snippet?: string }> = [];
  const seen = new Set<string>();
  for (const item of results) {
    const key = `${item.url || ''}\n${item.title || ''}`.trim();
    if (!key || seen.has(key)) continue;
    seen.add(key);
    deduped.push(item);
  }
  if (!query && deduped.length === 0 && !message) return null;
  return { query, results: deduped.slice(0, 5), message };
}

const SearchInlinePreview: React.FC<ToolInlinePreviewProps> = ({ toolCall, envelope }) => {
  const preview = getSearchPreview(toolCall, envelope);
  if (!preview) return <GenericInlinePreview toolCall={toolCall} envelope={envelope} />;
  return (
    <div className="min-w-0">
      {preview.query && (
        <div className="chat-text-wrap text-[11px] text-muted-foreground">
          <span className="text-muted-foreground/50">Search</span>{' '}
          <span className="font-mono text-muted-foreground">{preview.query}</span>
        </div>
      )}
      {preview.message && (
        <div className="chat-text-wrap mt-1 text-[11px] text-muted-foreground/70">
          {preview.message}
        </div>
      )}
      {preview.results.length > 0 && (
        <div className="mt-1 space-y-0.5">
          {preview.results.map((item, resultIndex) =>
            item.url ? (
              <a
                key={`${item.url || item.title}-${resultIndex}`}
                href={item.url}
                target="_blank"
                rel="noreferrer"
                onClick={(event) => event.stopPropagation()}
                className="flex min-w-0 items-center gap-1.5 text-[11px] text-muted-foreground/70 transition-colors hover:text-foreground"
              >
                <span className="text-muted-foreground/40">{resultIndex + 1}.</span>
                <span className="min-w-0 truncate">{item.title}</span>
                <ExternalLink size={10} className="shrink-0 text-muted-foreground/35" />
              </a>
            ) : (
              <div
                key={`${item.url || item.title}-${resultIndex}`}
                className="flex min-w-0 items-center gap-1.5 text-[11px] text-muted-foreground/70"
              >
                <span className="text-muted-foreground/40">{resultIndex + 1}.</span>
                <span className="min-w-0 truncate">{item.title}</span>
              </div>
            ),
          )}
        </div>
      )}
    </div>
  );
};

registerInlineToolPreview({
  match: (toolCall) => isSearchToolName(toolCall.name),
  component: SearchInlinePreview,
});

/* ------------------------------------------------------------------ */
/* generate_image preview — thumbnail + one line, never raw base64      */
/* ------------------------------------------------------------------ */

/**
 * The generic fallback would safeStringify the result envelope, and a
 * generate_image result whose `content` is a multi-megabyte data URI would
 * flood the dropdown with base64. This matcher claims the tool regardless of
 * envelope so even failures render a short line rather than the raw dump.
 */
const GENERATED_IMAGE_ERROR_LABELS: Record<GeneratedImageErrorCategory, string> = {
  auth: 'Image target needs attention',
  rate_limited: 'Rate limited — try again shortly',
  moderation_blocked: 'Blocked by the content policy',
  invalid_input: 'Invalid image request',
  provider_unavailable: 'Image provider unavailable',
  cancelled: 'Generation cancelled',
  unknown: 'Image generation failed',
};

function generatedImageErrorMessage(envelope: ToolResultEnvelope): string {
  const result = isPlainRecord(envelope.result) ? envelope.result : {};
  const explicit = firstString(
    typeof envelope.result === 'string' ? envelope.result : '',
    result.error,
    result.message,
    result.error_message,
  );
  return explicit || GENERATED_IMAGE_ERROR_LABELS[classifyGeneratedImageError(envelope)];
}

const GeneratedImageInlinePreview: React.FC<ToolInlinePreviewProps> = ({
  envelope,
  artifact,
}) => {
  const [thumbFailed, setThumbFailed] = React.useState(false);

  if (envelope && envelope.ok === false) {
    const message = compactText(generatedImageErrorMessage(envelope), 140);
    return (
      <div className="min-w-0 space-y-0.5 text-[11px] text-muted-foreground">
        <p className="font-medium text-red-400">Image generation failed</p>
        {message && <p className="chat-text-wrap text-red-400/80">{message}</p>}
      </div>
    );
  }

  const content = typeof artifact?.content === 'string' ? artifact.content : '';
  const isDataImage = /^data:image\//i.test(content);
  const meta = getGeneratedImageMetadata(artifact);
  const line = [
    meta.width && meta.height ? `${meta.width}×${meta.height}` : '',
    meta.format ? meta.format.toUpperCase() : '',
    meta.model,
  ]
    .filter(Boolean)
    .join(' · ');

  if (!isDataImage || thumbFailed) {
    return (
      <div className="min-w-0 space-y-0.5 text-[11px] text-muted-foreground">
        <p>{envelope ? 'Image generated' : 'Generating image…'}</p>
        {line && <p className="text-muted-foreground/70">{line}</p>}
      </div>
    );
  }

  return (
    <div className="min-w-0 space-y-1 text-[11px] text-muted-foreground">
      <img
        src={content}
        alt={generatedImageAltText(artifact)}
        onError={() => setThumbFailed(true)}
        className="max-h-40 max-w-full rounded-sm object-contain"
      />
      {line && <p className="text-muted-foreground/70">{line}</p>}
    </div>
  );
};

registerInlineToolPreview({
  match: (toolCall) => isImageGenerationToolName(toolCall.name),
  component: GeneratedImageInlinePreview,
});
