import React from "react";
import {
  AlertTriangle,
  CheckCircle2,
  ChevronRight,
  FolderOpen,
  FileText,
  BookOpen,
  BarChart2,
  Table as TableIcon,
  Database,
  Terminal,
  Loader2,
  Search,
  Code2,
  Globe2,
  Monitor,
  MessageSquare,
  Link2,
  Paperclip,
  Plus,
  ShieldCheck,
  XCircle,
} from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { StatusPill, type InspectorStatusKind } from "@/components/renderers/tool/query/atoms";
import { useToolActivityDisclosure } from "./toolActivity/useToolActivityDisclosure";
import { useCollapseAllToolActivity } from "@/lib/toolActivityPrefs";
import {
  claimToolCallReveal,
  peekToolCallReveal,
  subscribeToolCallReveal,
} from "@/lib/toolCallReveal";
import {
  resolveInlineToolPreview,
  type ToolInlinePreviewProps,
} from "./toolActivity/inlinePreviewRegistry";
import {
  arxivAuditSummary,
  arxivEnvelopeCached,
  arxivQueryOutcome,
  arxivQueryOutcomeLabel,
  arxivQueryText,
  arxivReadDetailCountParts,
  arxivReadInsightFromSources,
  arxivReadRegisterLabels,
  arxivReadShowsFindings,
  arxivScreeningEvidenceStats,
  arxivScreeningEvidenceUnverified,
  arxivScreeningSummary,
  buildArxivRunModel,
  buildArxivRunModelFromEnvelope,
  findArxivPaperTrace,
  type ArxivQueryTrace,
  type ArxivReadInsight,
  type ArxivRunModel,
} from "./toolActivity/arxivAuditModel";
import { getToolResultSummary } from "./toolActivity/resultSummary";
import { isToolResultFailure } from "./toolActivity/toolResultOutcome";
import { formatBytes } from "@/lib/formatters";
import type {
  Artifact,
  ChatFileAttachment,
  EnsureArtifactLoaded,
  MessageBlockReference,
  QueryJobStatus,
  ToolCall,
  ToolProgressEvent,
  ToolResultEnvelope,
} from "@/types";
import { isSandboxedHtmlArtifact } from "@/types";
import { isQueryRunToolName } from "@/lib/queryProviderRegistry";
import { isHiddenQueryToolCallName } from "@/lib/queryToolVisibility";
import { getQueryCardStateKey } from "@/lib/queryCardStateKey";
import { resolveQueryToolLifecycleStatus } from "@/lib/queryToolLifecycle";
import { isActiveQueryStatus } from "@/lib/queryProgress";
import QueryExecutionCard from "./QueryExecutionCard";
import GeneratedImageCard from "./GeneratedImageCard";
import { isImageGenerationToolName } from "@/lib/generatedImageArtifacts";
import ArtifactCard from "./ArtifactCard";
import FileAttachmentCard from "./FileAttachmentCard";
import HtmlArtifactCard from "./HtmlArtifactCard";
import MarkdownRenderer from "./MarkdownRenderer";
import ConnectionLogo from "./ConnectionLogo";
import { CONNECTION_META, getConnectionForToolName, type ConnectionMeta } from "@/lib/connectionsCatalog";
import { decideApproval } from "@/services/permissionsApi";
import "./toolAudit.css";
import {
  canDownloadArtifact,
  downloadArtifact,
  isRawDownloadArtifact,
} from "@/lib/artifactDownload";
import {
  fileAttachmentSnapshotArtifact,
  fileArtifactCardPresentation,
} from "@/lib/attachmentPresentation";
import {
  extractToolErrorDiagnosticFromEnvelope,
  formatToolErrorCardText,
} from "@/lib/toolErrorDiagnostics";

const INLINE_HTML_PREVIEW_LIMIT = 2;

interface ToolResultInfo {
  envelope?: ToolResultEnvelope;
  artifact?: Artifact;
  rawContent?: string;
  fileAttachments?: ChatFileAttachment[];
}

type ApprovalState = "pending" | "approved" | "rejected" | "error";

interface CollapsedToolsSummaryProps {
  toolCalls: ToolCall[];
  isLastMessage?: boolean;
  /** Whether the thread's run is still streaming. When explicitly false, a
      tool call without a result is treated as aborted (never pinned open) —
      otherwise a user Stop would leave the live container stuck on
      "Working" forever. Undefined preserves result-driven detection. */
  isThreadRunActive?: boolean;
  toolProgressByKey?: Record<string, ToolProgressEvent>;
  sqlToolResultsByScopedKey?: Map<
    string,
    {
      envelope: ToolResultEnvelope;
      artifact?: Artifact;
      resultMessageId?: string;
      resultMessageSeq?: number;
      resultMessageIdentityKey?: string;
    }
  >;
  messageIdentityKey?: string;
  artifactsById?: Record<string, Artifact>;
  ensureArtifactLoaded?: EnsureArtifactLoaded;
  onOpenArtifact: (a: Artifact) => void;
  onPinArtifact?: (a: Artifact) => void;
  pinnedArtifactIds?: string[];
  parentMessageId?: string;
  parentMessageSeq?: number;
  onToggleMessageReference?: (reference: MessageBlockReference) => void;
  pinnedMessageReferenceIds?: string[];
  toolResultsByCallId?: Map<string, ToolResultInfo>;
  compact?: boolean;
}

function findToolProgressForCall(
  progressByKey: Record<string, ToolProgressEvent> | undefined,
  runId: string,
  toolCallId: string,
): ToolProgressEvent | undefined {
  const callId = String(toolCallId || "").trim();
  if (!callId || !progressByKey) return undefined;
  const exact = runId ? progressByKey[`${runId}:${callId}`] : undefined;
  if (exact) return exact;

  // assistant_final carries the persisted tool call and can overwrite the
  // client-only _uiRunId before progress arrives. Tool-call IDs are unique,
  // so fall back to the newest matching event instead of dropping live state.
  let newest: ToolProgressEvent | undefined;
  for (const progress of Object.values(progressByKey)) {
    if (String(progress?.tool_call_id || "").trim() !== callId) continue;
    if (
      !newest ||
      Number(progress.timestamp || 0) >= Number(newest.timestamp || 0)
    ) {
      newest = progress;
    }
  }
  return newest;
}

function getToolIcon(toolName: string): {
  icon: React.ElementType;
  colorClass: string;
} {
  if (
    toolName === "jira_add_comment" ||
    toolName === "jira_reply_to_comment" ||
    toolName === "jira_create_issue" ||
    toolName === "jira_update_issue" ||
    toolName === "jira_add_related_tickets" ||
    toolName === "jira_upload_attachment" ||
    isGitHubWriteToolName(toolName) ||
    isSlackWriteToolName(toolName)
  ) {
    return { icon: ShieldCheck, colorClass: "text-amber-500" };
  }
  if (isSlackToolName(toolName)) {
    return { icon: MessageSquare, colorClass: "text-emerald-500" };
  }
  if (isGitHubToolName(toolName)) {
    return { icon: Terminal, colorClass: "text-muted-foreground" };
  }
  if (isAtlassianToolName(toolName)) {
    return { icon: Globe2, colorClass: "text-sky-500" };
  }
  if (isBrowserAutomationToolName(toolName)) {
    return { icon: Monitor, colorClass: "text-blue-500" };
  }
  if (toolName === "list_files" || toolName === "search_files") {
    return { icon: FolderOpen, colorClass: "text-artifact-file" };
  }
  if (toolName === "read_file") {
    return { icon: FileText, colorClass: "text-artifact-file" };
  }
  if (
    toolName === "get_echarts_capabilities" ||
    toolName === "get_highcharts_capabilities" ||
    toolName === "build_chart_artifact"
  ) {
    return { icon: BarChart2, colorClass: "text-artifact-chart" };
  }
  if (
    toolName === "create_interactive_artifact" ||
    toolName === "create_interactive_data_artifact"
  ) {
    return { icon: Code2, colorClass: "text-artifact-code" };
  }
  if (toolName === "display_table") {
    return { icon: TableIcon, colorClass: "text-artifact-data" };
  }
  if (isQueryRunToolName(toolName)) {
    return { icon: Database, colorClass: "text-artifact-data" };
  }
  if (toolName === "web_search") {
    return { icon: Search, colorClass: "text-blue-500" };
  }
  return { icon: Terminal, colorClass: "text-muted-foreground" };
}

function getArgsPreview(toolCall: ToolCall): string {
  const args = toolCall.args || {};
  if (typeof args.path === "string") return args.path as string;
  if (typeof args.sql === "string") {
    const sql = (args.sql as string).replace(/\s+/g, " ").trim();
    return sql.length > 60 ? sql.slice(0, 57) + "..." : sql;
  }
  if (typeof args.query === "string") return args.query as string;
  if (typeof args.title === "string") return args.title as string;
  if (typeof args.description === "string") return args.description as string;
  return "";
}

function compactText(value: unknown, maxLength = 120): string {
  const text = String(value || "").replace(/\s+/g, " ").trim();
  if (!text) return "";
  return text.length > maxLength ? `${text.slice(0, maxLength - 1).trimEnd()}…` : text;
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
    if (typeof value === "number" && Number.isFinite(value)) return String(value);
  }
  return "";
}

function formatDuration(ms?: number | null): string {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return "";
  if (ms < 1) return "<1ms";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = ms / 1000;
  if (seconds < 10) return `${seconds.toFixed(1)}s`;
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const minutes = Math.floor(seconds / 60);
  const rest = Math.round(seconds % 60);
  return `${minutes}m ${rest}s`;
}

/** Read UI-stamped start/end timing off a tool result envelope (see useChat). */
function getResultTiming(env?: ToolResultEnvelope): {
  startedAt?: number;
  endedAt?: number;
  durationMs?: number;
} {
  const result = env && isPlainRecord(env.result) ? env.result : null;
  const startedAt =
    result && typeof result._uiStartedAt === "number" ? result._uiStartedAt : undefined;
  const endedAt =
    result && typeof result._uiEndedAt === "number" ? result._uiEndedAt : undefined;
  const stampedDurationMs =
    typeof startedAt === "number" &&
    typeof endedAt === "number" &&
    endedAt >= startedAt
      ? endedAt - startedAt
      : undefined;
  const providerLatencyMs = Number((env?.result_metadata as any)?.latency_ms);
  const durationMs =
    Number.isFinite(providerLatencyMs) && providerLatencyMs > 0
      ? Math.max(stampedDurationMs ?? 0, providerLatencyMs)
      : stampedDurationMs;
  return { startedAt, endedAt, durationMs };
}

function envelopeErrorCardText(env?: ToolResultEnvelope): string | null {
  if (!isToolResultFailure(env)) return null;
  return formatToolErrorCardText(
    extractToolErrorDiagnosticFromEnvelope(env, {
      fallbackTitle: env?.tool_name || "Tool error",
    }),
  );
}

/** True when a completed (non-error) tool result reports cancellation. */
function isEnvelopeCancelled(env?: ToolResultEnvelope): boolean {
  if (!env || isToolResultFailure(env)) return false;
  const result = isPlainRecord(env.result) ? env.result : null;
  const status = String(
    result?.status || env.query_metadata?.status || "",
  ).toLowerCase();
  return status === "cancelled" || status === "canceled";
}

type ArxivRowState = "results" | "empty" | "paginated" | "saturated" | "screened" | "partial" | "unverified" | "cached" | "running" | "read";

type ArxivReadActivity = {
  kind: "pages" | "text";
  toolName: string;
  title: string;
  arxivId: string;
  paperKey: string;
  startPage?: number;
  endPage?: number;
  currentPage?: number;
  pageCount?: number;
  pagesRead?: number;
  hasMore?: boolean;
  nextPage?: number;
  requestedMaxPages?: number;
  offset?: number;
  totalChars?: number;
  charsRead?: number;
  extractQuality: string;
  sourceKind: string;
  insight: ArxivReadInsight;
  purposeTag: string;
  cached: boolean;
  untrustedNote: string;
};

function numberFromUnknown(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return undefined;
}

function firstNumber(...values: unknown[]): number | undefined {
  for (const value of values) {
    const parsed = numberFromUnknown(value);
    if (parsed !== undefined) return parsed;
  }
  return undefined;
}

function isArxivReadToolName(toolName: string): boolean {
  return toolName === "arxiv_read_paper" || toolName === "arxiv_get_paper_text";
}

function arxivReadActivity(
  tc: ToolCall,
  env?: ToolResultEnvelope,
  progress?: ToolProgressEvent,
): ArxivReadActivity | null {
  if (!isArxivReadToolName(tc.name)) return null;
  const args = isPlainRecord(tc.args) ? tc.args : {};
  const result = isPlainRecord(env?.result) ? env?.result : {};
  const pages = isPlainRecord(result?.paper_pages) ? result.paper_pages : {};
  const textWindow = isPlainRecord(result?.text_window) ? result.text_window : {};
  const pageSource = Object.keys(pages).length ? pages : {};
  const textSource = Object.keys(textWindow).length ? textWindow : {};
  const kind: ArxivReadActivity["kind"] =
    tc.name === "arxiv_read_paper" || Object.keys(pageSource).length ? "pages" : "text";

  const startPage = firstNumber(
    pageSource.start_page,
    pageSource.startPage,
    args.start_page,
    args.startPage,
  );
  // Mirror the backend clamp (max_pages capped at 20) so the live locator
  // never promises a wider window than the tool can return.
  const requestedMaxPagesRaw = firstNumber(args.max_pages, args.maxPages);
  const requestedMaxPages =
    requestedMaxPagesRaw !== undefined
      ? Math.min(Math.max(1, requestedMaxPagesRaw), 20)
      : undefined;
  const pageCount = firstNumber(
    progress?.page_count,
    progress?.pdf?.page_count,
    pageSource.page_count,
    pageSource.pageCount,
  );
  const currentPage = firstNumber(progress?.page_current, progress?.pdf?.page_current);
  const completedEndPage = firstNumber(pageSource.end_page, pageSource.endPage);
  const inferredEndPage =
    completedEndPage !== undefined
      ? completedEndPage
      : startPage !== undefined && requestedMaxPages !== undefined && requestedMaxPages > 0
        ? Math.min(startPage + Math.max(0, requestedMaxPages - 1), pageCount || startPage + requestedMaxPages - 1)
        : currentPage;
  const title = compactText(
    firstString(
      progress?.title,
      pageSource.title,
      textSource.title,
      args.title,
    ),
    120,
  );
  const arxivId = compactText(
    firstString(
      progress?.arxiv_id,
      pageSource.arxiv_id,
      pageSource.arxivId,
      textSource.arxiv_id,
      textSource.arxivId,
      args.paper,
    )
      .replace(/^arxiv:/i, "")
      .replace(/^https?:\/\/(?:www\.)?arxiv\.org\/(?:abs|pdf)\//i, "")
      .replace(/\.pdf$/i, "")
      .replace(/\/+$/, "")
      // Stored paper keys use "_" where old-style IDs use "/"; every Go
      // display surface is versionless, so strip vN for the locator too.
      .replace(/_/g, "/")
      .replace(/v\d+$/i, ""),
    64,
  );
  const sourceKind = compactText(
    firstString(
      progress?.source_kind,
      pageSource.source_kind,
      pageSource.sourceKind,
      textSource.source_kind,
      textSource.sourceKind,
    ),
    40,
  );
  const extractQuality = compactText(
    firstString(
      progress?.extract_quality,
      pageSource.extract_quality,
      pageSource.extractQuality,
      textSource.extract_quality,
      textSource.extractQuality,
    ),
    40,
  );
  const untrustedNote = compactText(
    firstString(
      pageSource.untrusted_note,
      pageSource.untrustedNote,
      textSource.untrusted_note,
      textSource.untrustedNote,
    ) || "arXiv paper text is untrusted external data.",
    160,
  );
  const insight = arxivReadInsightFromSources(progress, result, pageSource, textSource);

  return {
    kind,
    toolName: tc.name,
    title,
    arxivId,
    paperKey: compactText(firstString(progress?.paper_key, pageSource.paper_key, pageSource.paperKey, textSource.paper_key, textSource.paperKey), 80),
    startPage,
    endPage: inferredEndPage,
    currentPage,
    pageCount,
    // pages.length is the deterministic truth: the extractor can skip pages,
    // so end-start+1 over-counts on sparse windows.
    pagesRead: firstNumber(
      Array.isArray(pageSource.pages) && pageSource.pages.length > 0 ? pageSource.pages.length : undefined,
      progress?.pages_read,
    ),
    hasMore: pageSource.has_more === true,
    nextPage: firstNumber(pageSource.next_page),
    requestedMaxPages,
    offset: firstNumber(textSource.offset, args.offset),
    totalChars: firstNumber(textSource.total_chars, textSource.totalChars),
    charsRead: firstNumber(
      textSource.char_count,
      textSource.charCount,
      textSource.window_chars,
      textSource.windowChars,
      progress?.bytes_read,
      typeof textSource.text === "string" ? textSource.text.length : undefined,
    ),
    extractQuality,
    sourceKind,
    // The model-authored reading layer (motive/register, gloss, purpose,
    // takeaway, notes, chases) displays only when the backend emits it
    // (reserved today). Never read it from tc.args: the Go decoder rejects
    // unknown args, so an args value can only exist on a failed call.
    insight,
    purposeTag: compactText(insight.purpose_tag, 24)
      || (insight.register === "notes" && insight.register_explicit ? "SCAN" : "READ"),
    cached: Boolean(progress?.cached) || arxivEnvelopeCached(env),
    untrustedNote,
  };
}

function arxivReadGroupKey(
  tc: ToolCall,
  env?: ToolResultEnvelope,
): string {
  const info = arxivReadActivity(tc, env);
  if (!info) return "";
  return [
    info.paperKey || info.arxivId || info.title || tc.id || "",
    info.kind,
  ].join(":");
}

function arxivReadPageRange(info: ArxivReadActivity): string {
  if (info.kind === "text") {
    const offset = info.offset !== undefined ? `offset ${info.offset.toLocaleString()}` : "text window";
    return info.totalChars !== undefined && info.totalChars > 0
      ? `${offset} / ${info.totalChars.toLocaleString()} chars`
      : offset;
  }
  if (info.currentPage !== undefined && (!info.startPage || info.currentPage !== info.startPage)) {
    return info.pageCount ? `page ${info.currentPage} of ${info.pageCount}` : `page ${info.currentPage}`;
  }
  if (info.startPage !== undefined && info.endPage !== undefined && info.endPage >= info.startPage) {
    const prefix = info.startPage === info.endPage ? `p. ${info.startPage}` : `pp. ${info.startPage}–${info.endPage}`;
    return info.pageCount ? `${prefix} / ${info.pageCount}` : prefix;
  }
  return "paper pages";
}

function arxivReadLocator(info: ArxivReadActivity): string {
  const id = info.arxivId ? `arXiv:${info.arxivId}` : "arXiv paper";
  return `${id} · ${arxivReadPageRange(info)}`;
}

function arxivReadPrimaryLabel(info: ArxivReadActivity, running: boolean): string {
  if (info.title) return info.title;
  const verb = running ? "Reading" : "Read";
  return info.arxivId ? `${verb} arXiv:${info.arxivId}` : `${verb} arXiv paper`;
}

function arxivReadSubtitle(info: ArxivReadActivity, running: boolean): string {
  // Completed rows say nothing here: the locator line and the chip strip
  // already carry the range, source, and cache facts — repeating them as a
  // subtitle is pure noise. Live rows narrate the in-flight action.
  if (!running) return "";
  return info.currentPage
    ? `Reading ${arxivReadPageRange(info)}`
    : "Opening stored paper";
}

type ArxivReadLifecycleBeat = "fetch" | "read" | "reason" | "settled";

function arxivReadLifecycleBeat(
  running: boolean,
  progress?: ToolProgressEvent,
): ArxivReadLifecycleBeat {
  if (!running) return "settled";
  const phase = String(progress?.phase || "").toLowerCase();
  if (
    phase.includes("metadata") ||
    phase.includes("download") ||
    phase.includes("extract") ||
    phase.includes("cache")
  ) {
    return "fetch";
  }
  if (
    phase.includes("page_reading") ||
    phase.includes("window_reading") ||
    progress?.page_current ||
    progress?.pages_read
  ) {
    return "read";
  }
  return "reason";
}

function arxivReadLifecycleLabel(
  beat: ArxivReadLifecycleBeat,
  progress?: ToolProgressEvent,
  details = "",
): string {
  const message = compactText(progress?.message, 120);
  if (message) return message;
  const label = compactText(progress?.label, 90);
  if (label && beat !== "settled") return label;
  if (details && beat === "read") return details;
  switch (beat) {
    case "fetch":
      return "Fetching paper text";
    case "read":
      return "Reading paper";
    case "reason":
      return "Agent reasoning";
    default:
      return "Read settled";
  }
}

/** Intent tags mirror the design's tone map; the generic READ fallback stays
    muted so a tag chip only draws attention when it encodes real intent.
    SCAN stays in the muted family on purpose: it marks the low-commitment
    register, not a highlight. */
const ARXIV_PURPOSE_TAG_TONES: Record<string, string> = {
  SKIM: "border-muted-foreground/30 bg-muted/40 text-muted-foreground",
  SCAN: "border-muted-foreground/30 bg-muted/40 text-muted-foreground",
  TARGETED: "border-orange-500/30 bg-orange-500/10 text-orange-600 dark:text-orange-400",
  VERIFY: "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  "FULL READ": "border-orange-500/30 bg-orange-500/10 text-orange-600 dark:text-orange-400",
};

function ArxivReadPurposeTag({ value }: { value: string }) {
  const tag = value || "READ";
  const tone =
    ARXIV_PURPOSE_TAG_TONES[tag] || "border-muted-foreground/25 bg-muted/40 text-muted-foreground";
  return (
    <span className={`inline-flex shrink-0 items-center rounded border px-1.5 py-0.5 font-mono text-[9px] font-medium uppercase tracking-wide ${tone}`}>
      {tag}
    </span>
  );
}

function ArxivReadRuler({
  info,
  running = false,
  progressPercent = null,
  ariaValueText,
}: {
  info: ArxivReadActivity;
  running?: boolean;
  progressPercent?: number | null;
  ariaValueText?: string;
}) {
  if (info.kind === "text") {
    const totalChars = info.totalChars && info.totalChars > 0 ? info.totalChars : 0;
    if (!totalChars) return null;
    const offset = Math.max(0, info.offset || 0);
    const endOffset = Math.min(
      totalChars,
      offset + Math.max(0, info.charsRead || 0),
    );
    const completedPercent = Math.round((endOffset / totalChars) * 100);
    const progressValue =
      running && progressPercent !== null ? progressPercent : completedPercent;
    const coverageLabel =
      endOffset > offset
        ? `${(endOffset - offset).toLocaleString()} chars read · offset ${offset.toLocaleString()}-${endOffset.toLocaleString()} of ${totalChars.toLocaleString()}`
        : `offset ${offset.toLocaleString()} of ${totalChars.toLocaleString()} chars`;
    return (
      <div className="ta-clean-read-ruler" data-state={running ? "running" : "settled"}>
        <div
          className="ta-clean-read-charbar"
          role="progressbar"
          aria-label="Paper reading progress"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={progressValue}
          aria-valuetext={ariaValueText || coverageLabel}
        >
          <span
            className="ta-clean-read-charbar__fill"
            style={{ width: `${Math.max(0, Math.min(100, completedPercent))}%` }}
          />
        </div>
        <div className="ta-clean-read-ruler__label">{coverageLabel}</div>
      </div>
    );
  }

  const pageCount = info.pageCount && info.pageCount > 0 ? Math.min(Math.round(info.pageCount), 40) : 0;
  if (!pageCount || info.kind !== "pages") return null;
  const totalPages = Math.max(1, Math.round(info.pageCount || pageCount));
  const cells = Array.from({ length: pageCount }, (_, index) => index + 1);
  const toCell = (page?: number) => {
    if (!page || !Number.isFinite(page)) return undefined;
    return Math.max(1, Math.min(pageCount, Math.ceil((page / totalPages) * pageCount)));
  };
  const startCell = toCell(info.startPage) || toCell(info.currentPage);
  const runningEndPage =
    info.currentPage ||
    (info.startPage && info.pagesRead
      ? info.startPage + Math.max(0, info.pagesRead - 1)
      : undefined) ||
    info.startPage;
  const activeEndPage = running ? runningEndPage : info.endPage;
  const endCell = toCell(activeEndPage) || startCell;
  const currentCell = toCell(info.currentPage);
  // Prefer the deterministic count of pages actually returned; the start–end
  // span over-counts when the extractor skipped pages inside the window.
  const pagesCovered =
    info.pagesRead !== undefined
      ? info.pagesRead
      : info.startPage !== undefined && info.endPage !== undefined && info.endPage >= info.startPage
        ? info.endPage - info.startPage + 1
        : undefined;
  const coveredPercent =
    pagesCovered !== undefined && pagesCovered > 0
      ? Math.round((pagesCovered / totalPages) * 100)
      : 0;
  const coverageLabel =
    pagesCovered !== undefined && pagesCovered > 0
      ? `${pagesCovered.toLocaleString()} of ${totalPages.toLocaleString()} pages (${coveredPercent}%)`
      : "";
  return (
    <div className="ta-clean-read-ruler" data-state={running ? "running" : "settled"}>
      {/* Cells cap their width so a 6-page paper renders a short strip rather
          than the same full-width bar a 200-page paper gets. */}
      <div
        className="ta-clean-read-pagebar"
        role="progressbar"
        aria-label="Paper reading progress"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={
          running && progressPercent !== null ? progressPercent : coveredPercent
        }
        aria-valuetext={ariaValueText || coverageLabel || arxivReadPageRange(info)}
      >
        {cells.map((cell) => {
          const inRange = startCell !== undefined && endCell !== undefined && cell >= Math.min(startCell, endCell) && cell <= Math.max(startCell, endCell);
          const current = currentCell === cell;
          return (
            <span
              key={cell}
              className="ta-clean-read-pagebar__cell"
              data-state={current ? "current" : inRange ? "covered" : "empty"}
              aria-hidden="true"
            />
          );
        })}
      </div>
      {coverageLabel && (
        <div className="ta-clean-read-ruler__label">
          {coverageLabel}
        </div>
      )}
    </div>
  );
}

function ArxivReadInterstitial({
  label,
  percent,
}: {
  label: string;
  percent: number | null;
}) {
  return (
    <div className="ta-clean-read-interstitial" role="status" aria-live="polite">
      <span className="ta-clean-read-dots" aria-hidden="true">
        <span />
        <span />
        <span />
      </span>
      <span className="min-w-0 truncate">{label}</span>
      {percent !== null ? (
        <span className="ta-clean-read-percent">{percent}%</span>
      ) : null}
    </div>
  );
}

function isArxivToolName(toolName: string): boolean {
  return /^arxiv_/.test(String(toolName || ""));
}

function ArxivStatePill({ state }: { state: ArxivRowState }) {
  const config =
    state === "results"
      ? {
          label: "Results",
          icon: <CheckCircle2 size={11} aria-hidden />,
          className: "border-emerald-500/25 bg-emerald-500/10 text-emerald-400",
        }
      : state === "screened"
        ? {
            label: "Screened",
            icon: <CheckCircle2 size={11} aria-hidden />,
            className: "border-emerald-500/25 bg-emerald-500/10 text-emerald-400",
          }
        : state === "partial"
          ? {
              label: "Incomplete",
              icon: <AlertTriangle size={11} aria-hidden />,
              className: "border-amber-500/25 bg-amber-500/10 text-amber-400",
            }
        : state === "unverified"
          ? {
              label: "Evidence unverified",
              icon: <AlertTriangle size={11} aria-hidden />,
              className: "border-amber-500/25 bg-amber-500/10 text-amber-400",
            }
        : state === "paginated"
          ? {
              label: "More available",
              icon: <Search size={11} aria-hidden />,
              className: "border-sky-500/25 bg-sky-500/10 text-sky-400",
            }
        : state === "saturated"
          ? {
              label: "Saturated",
              icon: <Search size={11} aria-hidden />,
              className: "border-sky-500/25 bg-sky-500/10 text-sky-400",
            }
          : state === "read"
            ? {
                label: "Read",
                icon: <BookOpen size={11} aria-hidden />,
                className: "border-orange-500/25 bg-orange-500/10 text-orange-400",
              }
          : state === "cached"
            ? {
                label: "Cached",
                icon: <CheckCircle2 size={11} aria-hidden />,
                className: "border-muted-foreground/25 bg-muted/50 text-muted-foreground",
              }
            : state === "running"
              ? {
                  label: "Running",
                  icon: <Loader2 size={11} aria-hidden className="motion-safe:animate-spin" />,
                  className: "border-blue-500/25 bg-blue-500/10 text-blue-400",
                }
              : {
                  label: "0 results",
                  icon: <AlertTriangle size={11} aria-hidden />,
                  className: "border-amber-500/25 bg-amber-500/10 text-amber-400",
                };
  return (
    <span className={`inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[11px] font-medium ${config.className}`}>
      {config.icon}
      {config.label}
    </span>
  );
}

function arxivTraceForCall(model: ArxivRunModel | null, tc: ToolCall): ArxivQueryTrace | null {
  if (!model || tc.name !== "arxiv_search") return null;
  return model.queries.find((query) => query.tool_call_id === tc.id) || null;
}

function arxivScreeningIncomplete(model: ArxivRunModel | null): boolean {
  if (!model?.has_screening_report) return false;
  const kept = model.funnel.agent_kept_count ?? model.papers.filter((paper) => paper.status === "kept").length;
  const possible = model.funnel.agent_possible_count ?? model.papers.filter((paper) => paper.status === "possible").length;
  const excluded = model.funnel.agent_screened_out_count ?? model.papers.filter((paper) => paper.status === "screened_out").length;
  const notScreened = model.funnel.not_screened_count ?? model.papers.filter((paper) => paper.status === "not_screened").length;
  return notScreened > 0 || kept + possible + excluded === 0;
}

function arxivScreeningState(model: ArxivRunModel | null): ArxivRowState {
  if (arxivScreeningIncomplete(model)) return "partial";
  if (arxivScreeningEvidenceUnverified(model)) return "unverified";
  return "screened";
}

function arxivRowState(
  tc: ToolCall,
  env: ToolResultEnvelope | undefined,
  trace: ArxivQueryTrace | null,
  model: ArxivRunModel | null,
  isExec: boolean,
): ArxivRowState | null {
  if (!isArxivToolName(tc.name)) return null;
  if (isExec) return "running";
  // Read rows get a factual "Read" state, not a generic green Success: the
  // pill should state what happened, and the cache fact lives in the chip
  // strip where it sits next to source/quality.
  if (isArxivReadToolName(tc.name)) return env && !isToolResultFailure(env) ? "read" : null;
  if (arxivEnvelopeCached(env)) return "cached";
  if (tc.name === "arxiv_screening_report") return arxivScreeningState(model);
  if (tc.name !== "arxiv_search") return null;
  switch (arxivQueryOutcome(trace)) {
    case "starved":
      return "empty";
    case "paginated":
      return "paginated";
    case "saturated":
      return "saturated";
    case "results":
      return "results";
    default:
      return null;
  }
}

function arxivPrimaryLabel(tc: ToolCall, trace: ArxivQueryTrace | null): string {
  if (tc.name === "arxiv_search") {
    return arxivQueryText(trace?.args || tc.args) || "arXiv search";
  }
  if (tc.name === "arxiv_screening_report") return "screening report";
  return tc.name;
}

function arxivSubtitle(
  tc: ToolCall,
  trace: ArxivQueryTrace | null,
  model: ArxivRunModel | null,
  env?: ToolResultEnvelope,
): string {
  if (tc.name === "arxiv_search") {
    const base = arxivQueryOutcomeLabel(trace);
    const cached = arxivEnvelopeCached(env) ? "cached" : "";
    return [base, cached].filter(Boolean).join(" · ");
  }
	if (tc.name === "arxiv_screening_report") {
		return arxivScreeningSummary(model) || "Screened candidate papers";
	}
	return getToolResultSummary(tc, env) || "";
}

function aggregateDurationMs(toolCalls: ToolCall[], getEnvelope: (tc: ToolCall) => ToolResultEnvelope | undefined): number {
  return toolCalls.reduce((sum, tc) => {
    const durationMs = getResultTiming(getEnvelope(tc)).durationMs;
    return sum + (typeof durationMs === "number" ? durationMs : 0);
  }, 0);
}

function summarizeToolTimings(
  toolCalls: ToolCall[],
  getEnvelope: (tc: ToolCall) => ToolResultEnvelope | undefined,
): {
  spanMs?: number;
  summedDurationMs: number;
  missingCount: number;
  overlapLikely: boolean;
} {
  let minStart: number | undefined;
  let maxEnd: number | undefined;
  let summedDurationMs = 0;
  let missingCount = 0;
  for (const tc of toolCalls) {
    const timing = getResultTiming(getEnvelope(tc));
    if (typeof timing.startedAt === "number") {
      minStart = minStart === undefined ? timing.startedAt : Math.min(minStart, timing.startedAt);
    }
    if (typeof timing.endedAt === "number") {
      maxEnd = maxEnd === undefined ? timing.endedAt : Math.max(maxEnd, timing.endedAt);
    }
    if (typeof timing.durationMs === "number") {
      summedDurationMs += timing.durationMs;
    } else {
      missingCount += 1;
    }
  }
  const spanMs =
    typeof minStart === "number" && typeof maxEnd === "number" && maxEnd >= minStart
      ? maxEnd - minStart
      : undefined;
  return {
    spanMs,
    summedDurationMs,
    missingCount,
    overlapLikely: typeof spanMs === "number" && summedDurationMs > spanMs + 5,
  };
}

type ToolCardItem =
  | { kind: "tool"; tc: ToolCall; originalIndex: number }
  | { kind: "arxiv_empty_group"; calls: ToolCall[]; originalIndex: number }
  | { kind: "arxiv_read_group"; calls: ToolCall[]; originalIndex: number }
  | { kind: "arxiv_screening_group"; calls: ToolCall[]; originalIndex: number };

function isAtlassianToolName(toolName: string): boolean {
  return /^(atlassian_|jira_|confluence_)/.test(String(toolName || "").trim());
}

function isGitHubToolName(toolName: string): boolean {
  return /^github_/.test(String(toolName || "").trim());
}

function isGitHubWriteToolName(toolName: string): boolean {
  return /^(github_clone_repository|github_pull_repository|github_refresh_repository|github_run_gh_command)$/.test(String(toolName || "").trim());
}

function isSlackToolName(toolName: string): boolean {
  return /^slack_/.test(String(toolName || "").trim());
}

function isSlackWriteToolName(toolName: string): boolean {
  return /^(slack_post_message|slack_upload_file)$/.test(String(toolName || "").trim());
}

function isBrowserAutomationToolName(toolName: string): boolean {
  const name = String(toolName || "").trim().toLowerCase();
  if (!name) return false;
  return (
    isAtlassianToolName(name) ||
    name.includes("browser") ||
    name.includes("playwright") ||
    name.includes("computer") ||
    name.includes("cua") ||
    name.includes("screenshot") ||
    name.includes("click") ||
    name.includes("mouse") ||
    name.includes("keyboard") ||
    name.includes("locator")
  );
}

function humanizeName(value: string): string {
  const words = String(value || "")
    .replace(/[_-]+/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (!words) return "Run tool";
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function getActivityLabel(toolCall: ToolCall, progress?: ToolProgressEvent): string {
  const explicit = compactText(progress?.label, 90);
  if (explicit && progress?.phase !== "complete") return explicit;

  const args = toolCall.args || {};
  const issueKey = firstString(args.issueKey, (args as any).issue_key).toUpperCase();
  const spaceKey = firstString(args.spaceKey, (args as any).space_key).toUpperCase();
  const query = compactText(firstString(args.query, args.jql, (args as any).cql), 72);
  const url = compactText(firstString(args.url), 72);

  switch (toolCall.name) {
    case "web_search":
      return query ? `Search web: ${query}` : "Search web";
    case "cua_browser_status":
      return "Inspect browser state";
    case "cua_browser_navigate":
      return url ? `Open browser: ${url}` : "Open browser URL";
    case "cua_browser_snapshot":
      return "Read browser page";
    case "cua_browser_click":
      return "Click browser element";
    case "cua_browser_type":
      return "Type in browser";
    case "cua_browser_press":
      return "Press browser key";
    case "cua_browser_wait":
      return "Wait for browser";
    case "cua_browser_back":
      return "Go back in browser";
    case "cua_browser_logs":
      return "Read browser logs";
    case "cua_browser_clear_logs":
      return "Clear browser logs";
    case "atlassian_status":
      return "Inspect Atlassian browser state";
    case "atlassian_check_connection":
      return "Check Jira and Confluence connection";
    case "jira_get_issue":
      return issueKey ? `Open Jira issue ${issueKey}` : "Open Jira issue";
    case "jira_get_issue_history":
      return issueKey ? `Read Jira history for ${issueKey}` : "Read Jira issue history";
    case "jira_get_issue_comments":
      return issueKey ? `Read Jira comments for ${issueKey}` : "Read Jira comments";
    case "jira_search_issues":
      return query ? `Search Jira: ${query}` : "Search Jira issues";
    case "jira_find":
      return query ? `Find Jira objects: ${query}` : "Find Jira objects";
    case "jira_rest_get":
      return "Read Jira REST API";
    case "jira_add_comment":
      return issueKey ? `Review Jira comment for ${issueKey}` : "Review Jira comment";
    case "jira_reply_to_comment":
      return issueKey ? `Review Jira reply for ${issueKey}` : "Review Jira reply";
    case "jira_update_issue":
      return issueKey ? `Review Jira update for ${issueKey}` : "Review Jira update";
    case "jira_create_issue":
      return "Review Jira issue creation";
    case "confluence_search":
      return query ? `Search Confluence: ${query}` : "Search Confluence";
    case "confluence_open_page":
      return "Open Confluence page";
    case "confluence_open_space":
      return spaceKey ? `Open Confluence space ${spaceKey}` : "Open Confluence space";
    case "confluence_rest_get":
      return "Read Confluence REST API";
    case "github_clone_repository":
      return "Review GitHub clone";
    case "github_pull_repository":
      return "Review GitHub pull";
    case "github_refresh_repository":
      return "Review GitHub refresh";
    case "github_run_gh_command":
      return "Review GitHub CLI command";
    case "slack_post_message":
      return "Review Slack message";
    case "slack_upload_file":
      return "Review Slack file upload";
    default:
      return humanizeName(toolCall.name);
  }
}

function getProgressPercent(progress?: ToolProgressEvent): number | null {
  // The local search agent emits `pct`; everything else uses `percent`.
  const raw = Number(progress?.percent ?? progress?.pct);
  if (!Number.isFinite(raw)) return null;
  return Math.max(0, Math.min(100, Math.round(raw)));
}

function getGenericProgressLabel(progress?: ToolProgressEvent): string {
  const explicit = String(progress?.label || "").trim();
  if (explicit) return explicit;
  switch (progress?.phase) {
    case "browser_starting":
      return "Starting browser";
    case "browser_ready":
      return "Browser response ready";
    case "browser_navigating":
      return "Navigating";
    case "browser_searching":
      return "Searching";
    case "browser_reading":
      return "Reading page";
    case "approval_required":
      return "Awaiting approval";
    case "loading_source_metadata":
      return "Loading source metadata";
    case "source_loaded":
      return "Source data ready";
    case "binding_data_source":
      return "Binding data source";
    case "validating_html":
      return "Validating HTML";
    case "preparing_artifact":
      return "Preparing artifact";
    case "artifact_ready":
      return "Artifact ready";
    case "validation_failed":
      return "Validation failed";
    case "arxiv_metadata_lookup":
      return "Looking up arXiv metadata";
    case "arxiv_pdf_downloading":
      return "Downloading arXiv PDF";
    case "arxiv_pdf_downloaded":
      return "arXiv PDF downloaded";
    case "arxiv_pdf_extracting":
      return "Extracting PDF text";
    case "arxiv_pdf_page_reading":
      return "Reading paper";
    case "arxiv_text_window_reading":
      return "Opening stored paper";
    case "arxiv_paper_saved":
      return "Paper saved";
    case "arxiv_cache_hit":
      return "Using cached paper";
    case "local_search_agent_started":
      return "Starting local file search";
    case "local_search_agent_listing":
      return "Listing granted folder";
    case "local_search_agent_searching":
      return "Searching your files";
    case "local_search_agent_reading":
      return "Reading a matching file";
    case "local_search_agent_ranking":
      return "Ranking results";
    case "local_search_agent_completed":
      return "Local file search complete";
    case "local_search_agent_failed":
      return "Local file search failed";
    case "failed":
      return "Tool failed";
    default:
      return "Running";
  }
}

function getGenericProgressDetails(progress?: ToolProgressEvent): string {
  if (!progress) return "";
  const parts: string[] = [];
  const pageCurrent = Number(progress.page_current ?? progress.pdf?.page_current);
  const pageCount = Number(progress.page_count ?? progress.pdf?.page_count);
  if (Number.isFinite(pageCurrent) && pageCurrent > 0) {
    parts.push(Number.isFinite(pageCount) && pageCount > 0 ? `page ${pageCurrent} of ${pageCount}` : `page ${pageCurrent}`);
  }
  const title = compactText(progress.title, 90);
  if (title) {
    parts.push(title);
  }
  const fileRef = compactText(progress.file_ref, 70);
  if (fileRef) {
    parts.push(fileRef);
  }
  const extractQuality = compactText(progress.extract_quality, 40);
  if (extractQuality) {
    parts.push(extractQuality);
  }
  const sourceKind = compactText(progress.source_kind, 40);
  if (sourceKind && sourceKind !== extractQuality) {
    parts.push(sourceKind);
  }
  const rowCount = Number(progress.row_count);
  if (Number.isFinite(rowCount) && rowCount >= 0) {
    parts.push(`${rowCount.toLocaleString()} rows`);
  }
  const columnCount = Number(progress.column_count);
  if (Number.isFinite(columnCount) && columnCount >= 0) {
    parts.push(`${columnCount.toLocaleString()} columns`);
  }
  const bytes = Number(progress.html_bytes ?? progress.byte_size);
  if (Number.isFinite(bytes) && bytes > 0) {
    parts.push(`${formatBytes(bytes)} HTML`);
  }
  const sourceArtifactId = String(progress.source_artifact_id || "").trim();
  if (sourceArtifactId) {
    parts.push(`source ${sourceArtifactId.slice(0, 8)}`);
  }
  return parts.join(" · ");
}

function isPlainRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function getAtlassianApproval(envelope?: ToolResultEnvelope): Record<string, any> | null {
  if (!envelope) return null;
  const metadata = isPlainRecord(envelope.result_metadata)
    ? envelope.result_metadata
    : {};
  const result = isPlainRecord(envelope.result) ? envelope.result : {};
  const approval = result.approval;
  const isSupported =
    metadata.integration === "atlassian" ||
    metadata.integration === "github" ||
    metadata.integration === "slack" ||
    String(envelope.tool_name || "").startsWith("jira_") ||
    String(envelope.tool_name || "").startsWith("atlassian_") ||
    String(envelope.tool_name || "").startsWith("github_") ||
    String(envelope.tool_name || "").startsWith("slack_");
  const isApproval =
    metadata.widget === "approval" || isPlainRecord(approval);
  return isSupported && isApproval && isPlainRecord(approval) ? approval : null;
}

function approvalIntegration(envelope: ToolResultEnvelope): "atlassian" | "github" | "slack" {
  const metadata = isPlainRecord(envelope.result_metadata) ? envelope.result_metadata : {};
  if (metadata.integration === "github" || String(envelope.tool_name || "").startsWith("github_")) {
    return "github";
  }
  if (metadata.integration === "slack" || String(envelope.tool_name || "").startsWith("slack_")) {
    return "slack";
  }
  return "atlassian";
}

function formatApprovalAction(action: unknown): string {
  const raw = firstString(action);
  if (!raw) return "Write";
  return humanizeName(raw);
}

function approvalTargetLabel(target: Record<string, any>): string {
  const issue = firstString(target.issue_key, target.issueKey);
  const comment = firstString(target.comment_id, target.commentId);
  if (issue && comment) return `${issue} comment ${comment}`;
  return firstString(
    target.issue_key,
    target.issueKey,
    target.project_key,
    target.projectKey,
    target.repository,
    target.command,
    target.channel_id,
    target.channelId,
    target.project_id,
    target.projectId,
    target.key,
    target.id,
    "Target",
  );
}

function approvalSendAsLabel(approval: Record<string, any>): string {
  const content = isPlainRecord(approval.content) ? approval.content : {};
  const target = isPlainRecord(approval.target) ? approval.target : {};
  const sendAs = firstString(content.send_as, content.sendAs, target.send_as, target.sendAs);
  if (sendAs === "bot") return "Send as bot";
  if (sendAs === "user") return "Send as me";
  return "";
}

function approvalContentText(approval: Record<string, any>): {
  heading: string;
  body: string;
  issueType: string;
  replyToCommentId: string;
  sourceFormat: string;
  targetFormat: string;
} {
  const content = isPlainRecord(approval.content) ? approval.content : {};
  const related = Array.isArray(content.related_issue_keys)
    ? content.related_issue_keys.map((item) => firstString(item)).filter(Boolean)
    : [];
  const attachmentName = firstString(content.file_name, content.fileName);
  const attachmentSize = firstString(content.size_bytes, content.sizeBytes);
  const attachmentMime = firstString(content.mime_type, content.mimeType);
  const uploadPreview = attachmentName
    ? [
        `Attachment: ${attachmentName}`,
        attachmentSize ? `${attachmentSize} bytes` : "",
        attachmentMime,
      ].filter(Boolean).join(" · ")
    : "";
  const relatedPreview = related.length
    ? `Related tickets: ${related.join(", ")}${firstString(content.relationship) ? ` (${firstString(content.relationship)})` : ""}`
    : "";
  return {
    heading: firstString(content.summary, content.title),
    body: firstString(content.text, content.comment, content.description, content.changes, uploadPreview, relatedPreview, content.initial_comment, content.initialComment, content.command, Array.isArray(content.args) ? `gh ${content.args.join(" ")}` : "", approval.detail),
    issueType: firstString(content.issue_type, content.issueType),
    replyToCommentId: firstString(content.reply_to_comment_id, content.replyToCommentId),
    sourceFormat: firstString(content.source_format, content.sourceFormat),
    targetFormat: firstString(content.target_format, content.targetFormat),
  };
}

function AtlassianApprovalCard({ envelope }: { envelope: ToolResultEnvelope }) {
  const approval = getAtlassianApproval(envelope);
  const integration = approvalIntegration(envelope);
  const productLabel = integration === "github" ? "GitHub" : integration === "slack" ? "Slack" : "Jira";
  const [state, setState] = React.useState<ApprovalState>("pending");
  const [busy, setBusy] = React.useState<"approve" | "reject" | null>(null);
  const [detail, setDetail] = React.useState("");

  if (!approval) return null;

  const target = isPlainRecord(approval.target) ? approval.target : {};
  const content = approvalContentText(approval);
  const approvalID = firstString(approval.id);
  const actionName = firstString(approval.action);
  const sendAsLabel = approvalSendAsLabel(approval);
  const isCreate = actionName === "create_issue";
  const isUpdate = actionName === "update_issue";
  const Icon = integration === "github"
    ? ShieldCheck
    : integration === "slack"
      ? actionName === "upload_file"
        ? Paperclip
        : MessageSquare
    : isCreate
      ? Plus
      : isUpdate
        ? FileText
        : actionName === "add_related_tickets"
          ? Link2
          : actionName === "upload_attachment" || actionName === "upload_file"
            ? Paperclip
            : MessageSquare;
  const statusLabel =
    state === "pending"
      ? "Pending approval"
      : state === "approved"
        ? "Approved"
        : state === "rejected"
          ? "Rejected"
          : "Needs attention";

  const mutate = async (action: "approve" | "reject") => {
    if (!approvalID) {
      setState("error");
      setDetail("Missing approval ID.");
      return;
    }
    setBusy(action);
    try {
      // Unified approvals service first; falls back to the legacy
      // per-connector route while the backend migration is in flight.
      await decideApproval({
        approvalId: approvalID,
        action,
        legacyIntegration: integration,
      });
      const nextState = action === "approve" ? "approved" : "rejected";
      setState(nextState);
      setDetail(action === "approve" ? "Approved and executed." : `Rejected. ${productLabel} was not changed.`);
      toast.success(action === "approve" ? `${productLabel} action approved` : `${productLabel} action rejected`);
    } catch (err: any) {
      setState("error");
      setDetail(err?.message || "Approval action failed");
      toast.error(err?.message || "Approval action failed");
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 overflow-hidden">
      <div className="flex items-start justify-between gap-3 border-b border-amber-500/20 bg-amber-500/10 px-3 py-2.5">
        <div className="flex min-w-0 items-start gap-2.5">
          <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-amber-500/25 bg-background text-amber-600">
            <Icon size={16} />
          </div>
          <div className="min-w-0">
            <div className="text-sm font-semibold text-foreground">
              {firstString(approval.title) || `Review ${productLabel} change`}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {formatApprovalAction(approval.action)} · {approvalTargetLabel(target)}
              {sendAsLabel ? ` · ${sendAsLabel}` : ""}
            </div>
          </div>
        </div>
        <Badge
          variant="outline"
          className={
            state === "approved"
              ? "shrink-0 bg-emerald-500/10 text-emerald-600 border-emerald-500/25"
              : state === "rejected"
                ? "shrink-0 bg-muted text-muted-foreground border-border"
                : state === "error"
                  ? "shrink-0 bg-destructive/10 text-destructive border-destructive/25"
                  : "shrink-0 bg-amber-500/10 text-amber-700 border-amber-500/30"
          }
        >
          {statusLabel}
        </Badge>
      </div>

      <div className="space-y-3 p-3">
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <div className="rounded-md border border-border bg-background/70 px-3 py-2">
            <div className="text-[10px] font-medium uppercase text-muted-foreground/70">
              Target
            </div>
            <div className="mt-1 break-all font-mono text-xs text-foreground">
              {approvalTargetLabel(target)}
            </div>
            {sendAsLabel && (
              <div className="mt-1 text-[11px] text-muted-foreground">{sendAsLabel}</div>
            )}
          </div>
          <div className="rounded-md border border-border bg-background/70 px-3 py-2">
            <div className="text-[10px] font-medium uppercase text-muted-foreground/70">
              Action
            </div>
            <div className="mt-1 font-mono text-xs text-foreground">
              {formatApprovalAction(approval.action)}
              {content.issueType ? ` · ${content.issueType}` : ""}
            </div>
          </div>
        </div>

        <div className="rounded-md border border-border bg-background px-3 py-2.5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-[10px] font-medium uppercase text-muted-foreground/70">
              {productLabel} preview
            </div>
            {integration !== "github" && (content.sourceFormat || content.targetFormat) && (
              <Badge variant="outline" className="text-[10px]">
                {content.sourceFormat || "markdown"} -&gt; {content.targetFormat || "jira_adf"}
              </Badge>
            )}
          </div>
          {content.replyToCommentId && (
            <div className="mt-2 rounded-md border border-amber-500/20 bg-amber-500/5 px-2.5 py-1.5 text-xs text-muted-foreground">
              Replying to Jira comment{" "}
              <span className="font-mono text-foreground">{content.replyToCommentId}</span>
            </div>
          )}
          {content.heading && (
            <div className="mt-1 text-sm font-medium text-foreground">
              {content.heading}
            </div>
          )}
          <div className="mt-2 text-sm text-foreground [&_.chat-markdown]:max-w-none [&_.chat-markdown_pre]:my-2 [&_.chat-markdown_table]:my-2">
            {content.body ? (
              <MarkdownRenderer content={content.body} />
            ) : (
              <span className="text-muted-foreground">No content preview available.</span>
            )}
          </div>
        </div>

        <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-muted-foreground">
            {state === "pending"
              ? approvalID
                ? `Approval ID ${approvalID}`
                : "Approval ID unavailable"
              : detail}
          </div>
          {state === "pending" ? (
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!!busy || !approvalID}
                onClick={() => void mutate("reject")}
              >
                {busy === "reject" ? <Loader2 size={14} className="animate-spin" /> : <XCircle size={14} />}
                Reject
              </Button>
              <Button
                type="button"
                size="sm"
                disabled={!!busy || !approvalID}
                onClick={() => void mutate("approve")}
              >
                {busy === "approve" ? <Loader2 size={14} className="animate-spin" /> : <CheckCircle2 size={14} />}
                Approve
              </Button>
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function hasPlotSpecInArgs(tc: ToolCall): boolean {
  const args: any = tc.args || {};
  return !!(
    (args.echarts_spec &&
      typeof args.echarts_spec === "object" &&
      !Array.isArray(args.echarts_spec)) ||
    (args.chart_spec &&
      typeof args.chart_spec === "object" &&
      !Array.isArray(args.chart_spec))
  );
}

function getChartTypeFromSpec(spec: any): string | null {
  if (!spec || typeof spec !== "object") return null;
  const options = spec.options || spec;
  const series = options?.series;
  if (!Array.isArray(series) || series.length === 0) return null;
  return typeof series[0]?.type === "string" ? series[0].type : null;
}

function getChartSeriesCount(spec: any): number {
  if (!spec || typeof spec !== "object") return 0;
  const options = spec.options || spec;
  const series = options?.series;
  return Array.isArray(series) ? series.length : 0;
}

function getDataDimensions(
  dataJson: unknown,
): { rows: number; cols: number } | null {
  let arr: any[] | null = null;
  if (typeof dataJson === "string") {
    try {
      arr = JSON.parse(dataJson);
    } catch {
      return null;
    }
  } else if (Array.isArray(dataJson)) {
    arr = dataJson;
  }
  if (!Array.isArray(arr) || arr.length === 0) return null;
  const first = arr[0];
  const cols =
    first && typeof first === "object" && !Array.isArray(first)
      ? Object.keys(first).length
      : 0;
  return { rows: arr.length, cols };
}

function buildChartDetails(
  chartEngine: string | null,
  plotSpec: any,
  dataJson: unknown,
): string {
  const chartType = getChartTypeFromSpec(plotSpec);
  const seriesCount = getChartSeriesCount(plotSpec);
  const engineLabel =
    chartEngine === "echarts"
      ? "ECharts"
      : chartEngine === "highcharts"
        ? "Highcharts"
        : "Chart";
  const typePart = chartType || "chart";
  const dims = getDataDimensions(dataJson);
  const dataPart = dims ? `${dims.rows} pts` : "";
  const seriesPart = seriesCount > 1 ? `${seriesCount} series` : "";
  return [engineLabel, typePart, seriesPart, dataPart]
    .filter(Boolean)
    .join(" · ");
}

function artifactNeedsHydration(artifact: Artifact): boolean {
  return (
    artifact.metadata?.placeholder === true ||
    artifact.metadata?.partial === true ||
    artifact.metadata?.preview_only === true ||
    String(artifact.content || "").length === 0
  );
}

function addAttachmentArtifactIdValue(
  out: string[],
  seen: Set<string>,
  value: unknown,
) {
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (trimmed && !seen.has(trimmed)) {
      seen.add(trimmed);
      out.push(trimmed);
    }
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) addAttachmentArtifactIdValue(out, seen, item);
  }
}

function addAttachmentArtifactIdsFromRecord(
  out: string[],
  seen: Set<string>,
  record: Record<string, any>,
) {
  addAttachmentArtifactIdValue(out, seen, record.attachment_artifact_id);
  addAttachmentArtifactIdValue(out, seen, record.attachmentArtifactId);
  addAttachmentArtifactIdValue(out, seen, record.attachment_artifact_ids);
  addAttachmentArtifactIdValue(out, seen, record.attachmentArtifactIds);

  for (const listKey of ["attachments", "files", "downloads"]) {
    const list = record[listKey];
    if (!Array.isArray(list)) continue;
    for (const item of list) {
      if (!isPlainRecord(item)) continue;
      addAttachmentArtifactIdValue(out, seen, item.artifact_id);
      addAttachmentArtifactIdValue(out, seen, item.artifactId);
      addAttachmentArtifactIdValue(out, seen, item.attachment_artifact_id);
      addAttachmentArtifactIdValue(out, seen, item.attachmentArtifactId);
    }
  }
}

function toolResultAttachmentArtifactIds(info?: ToolResultInfo): string[] {
  if (!info?.envelope) return [];
  const out: string[] = [];
  const seen = new Set<string>();
  if (isPlainRecord(info.envelope.result)) {
    addAttachmentArtifactIdsFromRecord(out, seen, info.envelope.result);
  }
  if (isPlainRecord(info.envelope.result_metadata)) {
    addAttachmentArtifactIdsFromRecord(out, seen, info.envelope.result_metadata);
  }
  const mainArtifactId = String(info.envelope.artifact_id || "").trim();
  return mainArtifactId ? out.filter((id) => id !== mainArtifactId) : out;
}

/** Build an artifact from display_table tool call args so clicking opens it. */
function buildDisplayTableArtifact(
  tc: ToolCall,
  resultInfo?: ToolResultInfo,
): Artifact | null {
  const parsedArgs: any = tc.args || {};
  const parsedData = parsedArgs.data_json;
  const parsedEchartsSpec =
    parsedArgs.echarts_spec &&
    typeof parsedArgs.echarts_spec === "object" &&
    !Array.isArray(parsedArgs.echarts_spec)
      ? parsedArgs.echarts_spec
      : null;
  const parsedHighchartsSpec =
    parsedArgs.chart_spec &&
    typeof parsedArgs.chart_spec === "object" &&
    !Array.isArray(parsedArgs.chart_spec)
      ? parsedArgs.chart_spec
      : null;
  const parsedPlotSpec = parsedEchartsSpec || parsedHighchartsSpec;
  const parsedChartEngine = parsedEchartsSpec
    ? "echarts"
    : parsedHighchartsSpec
      ? "highcharts"
      : null;
  const parsedChartSpecVersion =
    typeof (
      parsedArgs.echarts_spec_version || parsedArgs.chart_spec_version
    ) === "string"
      ? parsedArgs.echarts_spec_version || parsedArgs.chart_spec_version
      : null;
  const parsedChartBudget =
    parsedArgs.chart_budget &&
    typeof parsedArgs.chart_budget === "object" &&
    !Array.isArray(parsedArgs.chart_budget)
      ? parsedArgs.chart_budget
      : null;

  if (!parsedData) {
    const resultArtifact = resultInfo?.artifact;
    if (!resultArtifact) return null;
    return {
      ...resultArtifact,
      title: parsedArgs.title || resultArtifact.title || "Data Visualization",
      metadata: {
        ...(resultArtifact.metadata || {}),
        tool_name: tc.name,
        tool_call_id: tc.id,
        ...(parsedPlotSpec ? { plot_spec: parsedPlotSpec } : {}),
        ...(parsedChartEngine ? { chart_engine: parsedChartEngine } : {}),
        ...(parsedChartSpecVersion
          ? { chart_spec_version: parsedChartSpecVersion }
          : {}),
        ...(parsedChartBudget ? { chart_budget: parsedChartBudget } : {}),
      },
    };
  }

  return {
    id: tc.id,
    type: "data",
    title: parsedArgs.title || "Data Visualization",
    content: parsedData,
    metadata: {
      tool_name: tc.name,
      tool_call_id: tc.id,
      ...(parsedPlotSpec ? { plot_spec: parsedPlotSpec } : {}),
      ...(parsedChartEngine ? { chart_engine: parsedChartEngine } : {}),
      ...(parsedChartSpecVersion
        ? { chart_spec_version: parsedChartSpecVersion }
        : {}),
      ...(parsedChartBudget ? { chart_budget: parsedChartBudget } : {}),
    },
  };
}

const CollapsedToolsSummary: React.FC<CollapsedToolsSummaryProps> = ({
  toolCalls,
  isLastMessage,
  isThreadRunActive,
  toolProgressByKey,
  sqlToolResultsByScopedKey,
  messageIdentityKey,
  artifactsById,
  ensureArtifactLoaded,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  parentMessageId,
  parentMessageSeq,
  onToggleMessageReference,
  pinnedMessageReferenceIds,
  toolResultsByCallId,
  compact,
}) => {
  // Filter internal helper calls out of the visible transcript.
  const visibleToolCalls = React.useMemo(
    () => toolCalls.filter((tc) => !isHiddenQueryToolCallName(tc.name)),
    [toolCalls],
  );
  const [queryStatusByScopedKey, setQueryStatusByScopedKey] = React.useState<
    Record<string, QueryJobStatus>
  >({});
  const handleQueryStatusChange = React.useCallback(
    (scopedKey: string, status: QueryJobStatus) => {
      setQueryStatusByScopedKey((previous) =>
        previous[scopedKey] === status
          ? previous
          : { ...previous, [scopedKey]: status },
      );
    },
    [],
  );

  // Merged groups (contiguous tool-only steps in the thinking flow) stamp
  // each call with its SOURCE message identity so historical SQL results
  // still resolve through the message-scoped key. Calls without a stamp
  // fall back to the group-level identity (single-message groups).
  const getCallMessageIdentityKey = React.useCallback(
    (tc: ToolCall): string | undefined => {
      const source = (tc as any)._uiSourceMessageIdentityKey;
      return typeof source === "string" && source.trim() ? source : messageIdentityKey;
    },
    [messageIdentityKey],
  );
  const hasApprovalRequest = React.useMemo(
    () =>
      visibleToolCalls.some((tc) => {
        const envelope = tc.id ? toolResultsByCallId?.get(tc.id)?.envelope : undefined;
        return !!getAtlassianApproval(envelope);
      }),
    [visibleToolCalls, toolResultsByCallId],
  );

  // Thread-level "collapse all tool activity" preference (persisted). The
  // toggle itself lives in the thread header (toolActivity/CollapseAllToggle);
  // groups only consume the value.
  const { collapseAll } = useCollapseAllToolActivity();

  // Determine if any tool is still running
  const isRunning = React.useMemo(() => {
    return visibleToolCalls.some((tc) => {
      const uiRunId =
        typeof (tc as any)._uiRunId === "string"
          ? (tc as any)._uiRunId.trim()
          : "";
      const hasImageProgress =
        isImageGenerationToolName(tc.name) &&
        Boolean(findToolProgressForCall(toolProgressByKey, uiRunId, tc.id));
      const hasStartedAt =
        typeof (tc as any)._uiStartedAt === "number" || hasImageProgress;
      const isSqlTool = isQueryRunToolName(tc.name);
      if (isSqlTool) {
        const scopedToolKey = getQueryCardStateKey({
          runId: (tc as any)._uiRunId,
          messageIdentityKey: getCallMessageIdentityKey(tc),
          toolCallId: tc.id,
        });
        const resultInfo = scopedToolKey
          ? sqlToolResultsByScopedKey?.get(scopedToolKey)
          : undefined;
        if (resultInfo) {
          const queryStatus = resolveQueryToolLifecycleStatus({
            envelope: resultInfo.envelope,
            artifact: resultInfo.artifact,
            liveStatus: scopedToolKey
              ? queryStatusByScopedKey[scopedToolKey]
              : undefined,
          });
          return isActiveQueryStatus(queryStatus);
        }
        return hasStartedAt && isLastMessage === true && isThreadRunActive !== false;
      }
      if (!hasStartedAt) return false;
      if (!isLastMessage || isThreadRunActive === false) return false;
      // For non-SQL tools, check if we have a result yet
      const hasResult = tc.id ? toolResultsByCallId?.has(tc.id) : false;
      return !hasResult;
    });
  }, [
    isLastMessage,
    isThreadRunActive,
    visibleToolCalls,
    getCallMessageIdentityKey,
    sqlToolResultsByScopedKey,
    queryStatusByScopedKey,
    toolResultsByCallId,
    toolProgressByKey,
  ]);

  // Resolve the result envelope for a tool call regardless of SQL vs generic path.
  const getEnvelopeForToolCall = React.useCallback(
    (tc: ToolCall): ToolResultEnvelope | undefined => {
      if (isQueryRunToolName(tc.name)) {
        const scopedToolKey = getQueryCardStateKey({
          runId: (tc as any)._uiRunId,
          messageIdentityKey: getCallMessageIdentityKey(tc),
          toolCallId: tc.id,
        });
        return scopedToolKey
          ? sqlToolResultsByScopedKey?.get(scopedToolKey)?.envelope
          : undefined;
      }
      return tc.id ? toolResultsByCallId?.get(tc.id)?.envelope : undefined;
    },
    [getCallMessageIdentityKey, sqlToolResultsByScopedKey, toolResultsByCallId],
  );

  // Distinct connectors touched across this group — drives the header logo stack.
  const groupConnectorMetas = React.useMemo(() => {
    const seen = new Set<string>();
    const metas: ConnectionMeta[] = [];
    for (const tc of visibleToolCalls) {
      const id = getConnectionForToolName(tc.name);
      if (!id || seen.has(id)) continue;
      seen.add(id);
      metas.push(CONNECTION_META[id]);
    }
    return metas;
  }, [visibleToolCalls]);

  // Propagate child completion stats and total span to the group header/pill.
  const groupStatus = React.useMemo(() => {
    let failedCount = 0;
    let cancelledCount = 0;
    let doneCount = 0;
    let minStart: number | undefined;
    let maxEnd: number | undefined;
    let toolDurationSumMs = 0;
    for (const tc of visibleToolCalls) {
      const isSqlTool = isQueryRunToolName(tc.name);
      if (isSqlTool) {
        const scopedKey = getQueryCardStateKey({
          runId: (tc as any)._uiRunId,
          messageIdentityKey: getCallMessageIdentityKey(tc),
          toolCallId: tc.id,
        });
        const resultInfo = scopedKey
          ? sqlToolResultsByScopedKey?.get(scopedKey)
          : undefined;
        const queryStatus = resolveQueryToolLifecycleStatus({
          envelope: resultInfo?.envelope,
          artifact: resultInfo?.artifact,
          liveStatus: scopedKey ? queryStatusByScopedKey[scopedKey] : undefined,
        });
        const isTerminal =
          queryStatus === "completed"
          || queryStatus === "failed"
          || queryStatus === "cancelled"
          || queryStatus === "expired"
          || queryStatus === "orphaned";
        if (isTerminal) {
          doneCount += 1;
          if (
            queryStatus === "failed"
            || queryStatus === "expired"
            || queryStatus === "orphaned"
          ) {
            failedCount += 1;
          } else if (queryStatus === "cancelled") {
            cancelledCount += 1;
          }
        }
      } else {
        const hasResult = tc.id ? !!toolResultsByCallId?.has(tc.id) : false;
        if (hasResult) doneCount += 1;
      }
      const env = getEnvelopeForToolCall(tc);
      if (!env) continue;
      if (!isSqlTool) {
        if (isToolResultFailure(env)) failedCount += 1;
        else if (isEnvelopeCancelled(env)) cancelledCount += 1;
      }
      const { startedAt, endedAt, durationMs: callDurationMs } = getResultTiming(env);
      if (typeof startedAt === "number") {
        minStart = minStart === undefined ? startedAt : Math.min(minStart, startedAt);
      }
      if (typeof endedAt === "number") {
        maxEnd = maxEnd === undefined ? endedAt : Math.max(maxEnd, endedAt);
      }
      if (typeof callDurationMs === "number") {
        toolDurationSumMs += callDurationMs;
      }
    }
    const durationMs =
      typeof minStart === "number" && typeof maxEnd === "number" && maxEnd >= minStart
        ? maxEnd - minStart
        : undefined;
    return { hasError: failedCount > 0, failedCount, cancelledCount, doneCount, durationMs, toolDurationSumMs };
  }, [
    visibleToolCalls,
    getEnvelopeForToolCall,
    getCallMessageIdentityKey,
    sqlToolResultsByScopedKey,
    queryStatusByScopedKey,
    toolResultsByCallId,
  ]);
  const groupDuration = formatDuration(
    typeof groupStatus.durationMs === "number" && groupStatus.durationMs > 0
      ? Math.max(groupStatus.durationMs, groupStatus.toolDurationSumMs)
      : groupStatus.toolDurationSumMs > 0
        ? groupStatus.toolDurationSumMs
        : groupStatus.durationMs,
  );

  const arxivToolCalls = React.useMemo(
    () => visibleToolCalls.filter((tc) => isArxivToolName(tc.name)),
    [visibleToolCalls],
  );
  const allVisibleToolsAreArxiv = arxivToolCalls.length > 0 && arxivToolCalls.length === visibleToolCalls.length;

  const arxivAuditModel = React.useMemo(() => {
    if (arxivToolCalls.length === 0) return null;
    const envelopes = new Map<string, ToolResultEnvelope | undefined>();
    let arxivDoneCount = 0;
    let arxivFailedCount = 0;
    for (const tc of arxivToolCalls) {
      if (tc.id) envelopes.set(tc.id, getEnvelopeForToolCall(tc));
      const env = getEnvelopeForToolCall(tc);
      if (env) arxivDoneCount += 1;
      if (isToolResultFailure(env)) arxivFailedCount += 1;
    }
    return buildArxivRunModel({
      toolCalls: arxivToolCalls,
      envelopesByCallId: envelopes,
      status: isRunning && arxivDoneCount < arxivToolCalls.length
        ? "live"
        : arxivFailedCount > 0
          ? "failed"
          : arxivDoneCount > 0
            ? "complete"
            : "partial",
    });
  }, [
    arxivToolCalls,
    getEnvelopeForToolCall,
    isRunning,
  ]);
  const arxivClockLine = React.useMemo(() => {
    if (!arxivAuditModel || arxivToolCalls.length === 0) return "";
    const timing = summarizeToolTimings(arxivToolCalls, getEnvelopeForToolCall);
    const parts: string[] = [];
    if (typeof timing.spanMs === "number") {
      parts.push(`observed arXiv span ${formatDuration(timing.spanMs) || "<1ms"}`);
    }
    if (timing.summedDurationMs > 0) {
      parts.push(`summed measured calls ${formatDuration(timing.summedDurationMs)}`);
    }
    if (typeof timing.spanMs === "number" && timing.summedDurationMs <= timing.spanMs) {
      const residualMs = timing.spanMs - timing.summedDurationMs;
      if (residualMs > 0) parts.push(`estimated non-tool gap ~${formatDuration(residualMs)}`);
    }
    if (timing.overlapLikely) parts.push("overlap/concurrency likely");
    if (timing.missingCount > 0) parts.push(`${timing.missingCount} timing ${timing.missingCount === 1 ? "field" : "fields"} missing`);
    return parts.join(" · ");
  }, [arxivAuditModel, arxivToolCalls, getEnvelopeForToolCall]);

  // ----- Disclosure state machine: minimized pill / expanded / live-pinned -----
  const containerRef = React.useRef<HTMLDivElement | null>(null);
  const pillRef = React.useRef<HTMLButtonElement | null>(null);
  const pointerInsideRef = React.useRef(false);

  const disclosure = useToolActivityDisclosure({
    stateKey: messageIdentityKey ? `tool-activity:${messageIdentityKey}` : undefined,
    isRunning,
    hasFailure: groupStatus.failedCount > 0,
    hasPendingApproval: hasApprovalRequest,
    collapseAll,
    shouldDeferCollapse: () => {
      if (pointerInsideRef.current) return true;
      const node = containerRef.current;
      return !!(node && document.activeElement && node.contains(document.activeElement));
    },
  });
  const { isOpen, isPinned } = disclosure;

  // Keyboard continuity: the pill <-> expanded swap unmounts the button the
  // user just activated, dropping focus on <body>. Restore focus onto the
  // now-visible toggle — but only when the change came from an explicit
  // toggle inside the container (armed below), never on auto-collapse, which
  // would steal focus from wherever the user is on the page.
  const headerToggleRef = React.useRef<HTMLButtonElement | null>(null);
  const focusRestoreArmedRef = React.useRef(false);
  const wasOpenRef = React.useRef(isOpen);
  React.useEffect(() => {
    const wasOpen = wasOpenRef.current;
    wasOpenRef.current = isOpen;
    if (wasOpen === isOpen) return;
    if (!focusRestoreArmedRef.current) return;
    focusRestoreArmedRef.current = false;
    const target = isOpen ? headerToggleRef.current : pillRef.current;
    target?.focus({ preventScroll: true });
  }, [isOpen]);

  const setOpenWithFocusRestore = React.useCallback(
    (next: boolean) => {
      focusRestoreArmedRef.current = true;
      disclosure.setOpen(next);
    },
    [disclosure.setOpen],
  );

  // ----- Origin-event reveal (files audit -> "Open origin chat") -----
  // A provenance surface requested a specific tool_call_id; if this group owns
  // it, claim the request, expand from the pill, then scroll to and flash the
  // exact card. Checked on mount (thread just rendered) and on notification
  // (group already mounted when the request lands).
  const [revealFlash, setRevealFlash] = React.useState<{ callId: string; token: number } | null>(null);
  const revealScrollPendingRef = React.useRef(false);
  const revealTokenRef = React.useRef(0);
  const revealFlashTimerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  const visibleToolCallIds = React.useMemo(() => {
    const ids = new Set<string>();
    for (const tc of visibleToolCalls) {
      if (tc.id) ids.add(tc.id);
    }
    return ids;
  }, [visibleToolCalls]);
  React.useEffect(() => {
    const tryClaim = () => {
      const pendingId = peekToolCallReveal();
      if (!pendingId || !visibleToolCallIds.has(pendingId)) return;
      if (!claimToolCallReveal(pendingId)) return;
      disclosure.setOpen(true);
      revealScrollPendingRef.current = true;
      revealTokenRef.current += 1;
      setRevealFlash({ callId: pendingId, token: revealTokenRef.current });
      if (revealFlashTimerRef.current !== null) clearTimeout(revealFlashTimerRef.current);
      revealFlashTimerRef.current = setTimeout(() => setRevealFlash(null), 2400);
    };
    tryClaim();
    return subscribeToolCallReveal(tryClaim);
  }, [visibleToolCallIds, disclosure.setOpen]);
  React.useEffect(() => () => {
    if (revealFlashTimerRef.current !== null) clearTimeout(revealFlashTimerRef.current);
  }, []);
  React.useEffect(() => {
    if (!revealFlash || !revealScrollPendingRef.current) return;
    revealScrollPendingRef.current = false;
    // One frame so the expanded cards have committed layout before scrolling.
    const frame = requestAnimationFrame(() => {
      const container = containerRef.current;
      if (!container) return;
      let target: HTMLElement | null = null;
      try {
        target = container.querySelector<HTMLElement>(
          `[data-tool-call-anchor~="${CSS.escape(revealFlash.callId)}"]`,
        );
      } catch {
        target = null;
      }
      const node = target || container;
      const reduceMotion = typeof window.matchMedia === "function"
        && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      node.scrollIntoView({ block: "center", behavior: reduceMotion ? "auto" : "smooth" });
      if (node === target) node.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [revealFlash]);

  // Screen-reader phase announcements — boundaries only, never ticks.
  // Ref starts false so a group already running at mount (tool calls stream
  // in pre-stamped) still announces its start once.
  const [announcement, setAnnouncement] = React.useState("");
  const announceRunningRef = React.useRef(false);
  React.useEffect(() => {
    const was = announceRunningRef.current;
    announceRunningRef.current = isRunning;
    const total = visibleToolCalls.length;
    if (!was && isRunning) {
      setAnnouncement(`Running ${total} tool${total === 1 ? "" : "s"}`);
      return;
    }
    if (was && !isRunning) {
      if (groupStatus.failedCount > 0) {
        setAnnouncement(`Finished: ${groupStatus.failedCount} of ${total} tools failed`);
      } else if (groupStatus.cancelledCount > 0) {
        setAnnouncement(`Finished: ${groupStatus.cancelledCount} of ${total} tools cancelled`);
      } else if (groupStatus.doneCount !== total) {
        setAnnouncement(`Stopped before ${total - groupStatus.doneCount} of ${total} tools completed`);
      } else {
        const durationPart = groupDuration ? ` in ${groupDuration}` : "";
        setAnnouncement(
          total === 1
            ? `Finished: 1 tool succeeded${durationPart}`
            : `Finished: ${total} tools succeeded${durationPart}`,
        );
      }
    }
  }, [
    isRunning,
    groupStatus.failedCount,
    groupStatus.cancelledCount,
    groupStatus.doneCount,
    groupDuration,
    visibleToolCalls.length,
  ]);

  // Per-card inline preview disclosure.
  const [openPreviewByKey, setOpenPreviewByKey] = React.useState<Record<string, boolean>>({});
  const togglePreview = React.useCallback((key: string) => {
    setOpenPreviewByKey((prev) => ({ ...prev, [key]: !prev[key] }));
  }, []);
  const contentId = React.useId();

  // Collect ALL chart artifacts — from display_table args AND SQL query results with plot_spec
  const chartArtifacts = React.useMemo(() => {
    const items: Array<{
      key: string;
      artifact: Artifact;
      plotSpec: any;
      chartEngine: string | null;
      title: string;
      dataJson?: unknown;
    }> = [];

    for (const tc of visibleToolCalls) {
      if (isArxivToolName(tc.name)) {
        continue;
      }

      // display_table with a chart spec in args
      if (tc.name === "display_table" && hasPlotSpecInArgs(tc)) {
        const resultInfo = tc.id ? toolResultsByCallId?.get(tc.id) : undefined;
        const artifact = buildDisplayTableArtifact(tc, resultInfo);
        if (!artifact) continue;
        const args: any = tc.args || {};
        const plotSpec =
          args.echarts_spec ||
          args.chart_spec ||
          (artifact.metadata as any)?.plot_spec ||
          (resultInfo?.envelope as any)?.plot_spec ||
          null;
        if (!plotSpec) continue;
        const chartEngine = args.echarts_spec
          ? "echarts"
          : args.chart_spec
            ? "highcharts"
            : ((artifact.metadata as any)?.chart_engine as string | null) ||
              ((resultInfo?.envelope as any)?.chart_engine as string | null) ||
              null;
        items.push({
          key: `dt-${tc.id}`,
          artifact,
          plotSpec,
          chartEngine,
          title: args.title || artifact.title || "Chart",
          dataJson:
            args.data_json !== undefined ? args.data_json : artifact.content,
        });
        continue;
      }

      // SQL query results that carry a plot_spec (in artifact metadata or envelope)
      if (isQueryRunToolName(tc.name)) {
        const scopedToolKey = getQueryCardStateKey({
          runId: (tc as any)._uiRunId,
          messageIdentityKey: getCallMessageIdentityKey(tc),
          toolCallId: tc.id,
        });
        const resultInfo = scopedToolKey
          ? sqlToolResultsByScopedKey?.get(scopedToolKey)
          : undefined;
        if (!resultInfo) continue;
        const artifact =
          resultInfo.artifact ||
          (resultInfo.envelope?.artifact_id
            ? artifactsById?.[resultInfo.envelope.artifact_id]
            : undefined);
        if (!artifact) continue;
        const plotSpec =
          artifact.metadata?.plot_spec || resultInfo.envelope?.plot_spec;
        if (!plotSpec) continue;
        const chartEngine =
          artifact.metadata?.chart_engine ||
          resultInfo.envelope?.chart_engine ||
          null;
        items.push({
          key: `sql-${tc.id}`,
          artifact,
          plotSpec,
          chartEngine,
          title: artifact.title || "Query Chart",
          dataJson: artifact.content,
        });
        continue;
      }

      // General tool results (e.g. build_chart_artifact) that carry plot_spec
      if (tc.name !== "display_table") {
        const resultInfo = tc.id ? toolResultsByCallId?.get(tc.id) : undefined;
        if (!resultInfo) continue;
        const artifact =
          resultInfo.artifact ||
          (resultInfo.envelope?.artifact_id
            ? artifactsById?.[resultInfo.envelope.artifact_id]
            : undefined);
        if (!artifact) continue;
        const plotSpec =
          (artifact.metadata as any)?.plot_spec ||
          (resultInfo.envelope as any)?.plot_spec;
        if (!plotSpec) continue;
        const chartEngine =
          (artifact.metadata as any)?.chart_engine ||
          (resultInfo.envelope as any)?.chart_engine ||
          null;
        items.push({
          key: `tool-${tc.id}`,
          artifact,
          plotSpec,
          chartEngine,
          title: artifact.title || tc.name,
          dataJson: artifact.content,
        });
        continue;
      }
    }

    return items;
  }, [
    visibleToolCalls,
    getCallMessageIdentityKey,
    sqlToolResultsByScopedKey,
    artifactsById,
    toolResultsByCallId,
  ]);

  const htmlArtifacts = React.useMemo(() => {
    const items: Array<{ key: string; artifact: Artifact; title: string }> = [];
    const seen = new Set<string>();

    for (const tc of visibleToolCalls) {
      if (isArxivToolName(tc.name)) {
        continue;
      }
      if (isQueryRunToolName(tc.name) || tc.name === "display_table") {
        continue;
      }
      const resultInfo = tc.id ? toolResultsByCallId?.get(tc.id) : undefined;
      const artifact =
        resultInfo?.artifact ||
        (resultInfo?.envelope?.artifact_id
          ? artifactsById?.[resultInfo.envelope.artifact_id]
          : undefined);
      if (!artifact || !isSandboxedHtmlArtifact(artifact, tc.name)) continue;
      const key = artifact.id || tc.id;
      if (!key || seen.has(key)) continue;
      seen.add(key);
      items.push({
        key: `html-${key}`,
        artifact,
        title: artifact.title || getArgsPreview(tc) || "HTML Artifact",
      });
    }

    return items;
  }, [artifactsById, toolResultsByCallId, visibleToolCalls]);

  const generatedFileArtifacts = React.useMemo(() => {
    const items: Array<{ key: string; artifact: Artifact }> = [];
    const seen = new Set<string>();
    const addArtifact = (artifact: Artifact | undefined, fallbackId?: string) => {
      if (!artifact || artifact.type !== "file") return;
      if (!isRawDownloadArtifact(artifact) && !canDownloadArtifact(artifact)) return;
      const key = artifact.id || fallbackId || "";
      if (!key || seen.has(key)) return;
      seen.add(key);
      items.push({ key: `file-${key}`, artifact });
    };
    const snapshotByArtifactId = (resultInfo?: ToolResultInfo) => {
      const out = new Map<string, ChatFileAttachment>();
      for (const attachment of resultInfo?.fileAttachments || []) {
        const id = String(attachment.artifactId || "").trim();
        if (id) out.set(id, attachment);
      }
      return out;
    };
    const addSnapshot = (attachment: ChatFileAttachment | undefined) => {
      const artifact = attachment ? fileAttachmentSnapshotArtifact(attachment) : null;
      if (artifact) addArtifact(artifact, attachment?.artifactId);
    };

    for (const tc of visibleToolCalls) {
      const resultInfo = tc.id ? toolResultsByCallId?.get(tc.id) : undefined;
      const directArtifact =
        resultInfo?.artifact ||
        (resultInfo?.envelope?.artifact_id
            ? artifactsById?.[resultInfo.envelope.artifact_id]
            : undefined);
      addArtifact(directArtifact, resultInfo?.envelope?.artifact_id);

      const snapshots = snapshotByArtifactId(resultInfo);
      for (const artifactId of toolResultAttachmentArtifactIds(resultInfo)) {
        addArtifact(artifactsById?.[artifactId], artifactId);
        if (!artifactsById?.[artifactId]) addSnapshot(snapshots.get(artifactId));
      }
      for (const attachment of resultInfo?.fileAttachments || []) {
        if (!artifactsById?.[attachment.artifactId]) addSnapshot(attachment);
      }
    }

    return items;
  }, [artifactsById, toolResultsByCallId, visibleToolCalls]);

  const previewHtmlArtifacts = React.useMemo(
    () => htmlArtifacts.slice(0, INLINE_HTML_PREVIEW_LIMIT),
    [htmlArtifacts],
  );

  const [hydratedHtmlArtifactsById, setHydratedHtmlArtifactsById] =
    React.useState<Record<string, Artifact>>({});

  React.useEffect(() => {
    if (!ensureArtifactLoaded) return;
    const targets = previewHtmlArtifacts
      .map((item) => item.artifact)
      .filter((artifact) => artifactNeedsHydration(artifact));
    if (targets.length === 0) return;

    let cancelled = false;
    for (const artifact of targets) {
      void ensureArtifactLoaded(artifact.id)
        .then((loaded) => {
          if (!loaded || cancelled) return;
          if (
            !isSandboxedHtmlArtifact(
              loaded,
              String(artifact.metadata?.tool_name || "").trim(),
            )
          ) {
            return;
          }
          setHydratedHtmlArtifactsById((prev) => ({
            ...prev,
            [artifact.id]: loaded,
          }));
        })
        .catch(() => {});
    }

    return () => {
      cancelled = true;
    };
  }, [ensureArtifactLoaded, previewHtmlArtifacts]);

  if (visibleToolCalls.length === 0) return null;

  const runActive = isThreadRunActive !== false;
  const totalCount = visibleToolCalls.length;
  const toolNoun = totalCount === 1 ? "tool" : "tools";
  const defaultGroupTitle = `Ran ${totalCount} ${toolNoun}`;
  const allVisibleToolsAreArxivReads =
    allVisibleToolsAreArxiv && visibleToolCalls.every((tc) => isArxivReadToolName(tc.name));
  const arxivWorkingLabel = allVisibleToolsAreArxivReads
    ? "Reading arXiv paper"
    : allVisibleToolsAreArxiv
      ? "Searching arXiv"
      : arxivAuditModel
        ? "Working · arXiv active"
        : "Working";
  const arxivSummary = arxivAuditSummary(arxivAuditModel);
  const groupTitle = arxivSummary
    ? allVisibleToolsAreArxiv
      ? arxivSummary
      : `${defaultGroupTitle} · ${arxivSummary}`
    : allVisibleToolsAreArxivReads
      ? totalCount === 1
        ? "Reading arXiv paper"
        : `Reading ${totalCount} arXiv paper windows`
    : defaultGroupTitle;
  const { failedCount, cancelledCount, doneCount } = groupStatus;
  // Failure copy always carries the exact denominator ("1 of 2 failed") so
  // minimizing a group can never understate how much of it failed.
  const failedSummaryText =
    failedCount > 0
      ? totalCount === 1
        ? "failed"
        : `${failedCount} of ${totalCount} failed`
      : "";

  const logoStack = groupConnectorMetas.length > 0 && (
    <>
      <span className="ta-scope ta-stack" aria-hidden="true">
        {groupConnectorMetas.slice(0, 3).map((meta) => (
          <span
            key={meta.id}
            className={`ta-lg${meta.mono ? " ta-mono" : ""}`}
            title={meta.label}
          >
            <ConnectionLogo meta={meta} />
          </span>
        ))}
      </span>
      {groupConnectorMetas.length > 3 && (
        <span className="text-[10px] text-muted-foreground" aria-hidden="true">
          +{groupConnectorMetas.length - 3}
        </span>
      )}
    </>
  );

  const toolCardItems = React.useMemo<ToolCardItem[]>(() => {
    const items: ToolCardItem[] = [];
    let index = 0;
    while (index < visibleToolCalls.length) {
      const tc = visibleToolCalls[index];
      const trace = arxivTraceForCall(arxivAuditModel, tc);
      const readEnvelope = getEnvelopeForToolCall(tc);
      const readGroupKey =
        isArxivReadToolName(tc.name) &&
        readEnvelope &&
        !isToolResultFailure(readEnvelope)
          ? arxivReadGroupKey(tc, readEnvelope)
          : "";
      if (readGroupKey) {
        const calls: ToolCall[] = [];
        const startIndex = index;
        while (index < visibleToolCalls.length) {
          const candidate = visibleToolCalls[index];
          const envelope = getEnvelopeForToolCall(candidate);
          const candidateKey =
            isArxivReadToolName(candidate.name) &&
            envelope &&
            !isToolResultFailure(envelope)
              ? arxivReadGroupKey(candidate, envelope)
              : "";
          if (candidateKey !== readGroupKey) break;
          calls.push(candidate);
          index += 1;
        }
        if (calls.length >= 2) {
          items.push({ kind: "arxiv_read_group", calls, originalIndex: startIndex });
        } else {
          items.push({ kind: "tool", tc: calls[0], originalIndex: startIndex });
        }
        continue;
      }

      const isStarvedSearch =
        tc.name === "arxiv_search" &&
        !isToolResultFailure(getEnvelopeForToolCall(tc)) &&
        arxivQueryOutcome(trace) === "starved";
      if (isStarvedSearch) {
        const calls: ToolCall[] = [];
        const startIndex = index;
        while (index < visibleToolCalls.length) {
          const candidate = visibleToolCalls[index];
          const candidateTrace = arxivTraceForCall(arxivAuditModel, candidate);
          if (
            candidate.name !== "arxiv_search" ||
            isToolResultFailure(getEnvelopeForToolCall(candidate)) ||
            arxivQueryOutcome(candidateTrace) !== "starved"
          ) {
            break;
          }
          calls.push(candidate);
          index += 1;
        }
        if (calls.length >= 2) {
          items.push({ kind: "arxiv_empty_group", calls, originalIndex: startIndex });
        } else {
          items.push({ kind: "tool", tc: calls[0], originalIndex: startIndex });
        }
        continue;
      }

      if (tc.name === "arxiv_screening_report" && !isToolResultFailure(getEnvelopeForToolCall(tc))) {
        const calls: ToolCall[] = [];
        const startIndex = index;
        while (index < visibleToolCalls.length) {
          const candidate = visibleToolCalls[index];
          if (candidate.name !== "arxiv_screening_report" || isToolResultFailure(getEnvelopeForToolCall(candidate))) {
            break;
          }
          calls.push(candidate);
          index += 1;
        }
        if (calls.length >= 2) {
          items.push({ kind: "arxiv_screening_group", calls, originalIndex: startIndex });
        } else {
          items.push({ kind: "tool", tc: calls[0], originalIndex: startIndex });
        }
        continue;
      }

      items.push({ kind: "tool", tc, originalIndex: index });
      index += 1;
    }
    return items;
  }, [arxivAuditModel, getEnvelopeForToolCall, visibleToolCalls]);

  const toolCards = toolCardItems.map((item, idx) => {
              if (item.kind === "arxiv_read_group") {
                const activities = item.calls
                  .map((call) => arxivReadActivity(call, getEnvelopeForToolCall(call)))
                  .filter(Boolean) as ArxivReadActivity[];
                const first = activities[0];
                const last = activities[activities.length - 1] || first;
                if (!first) return null;

                const paperTrace = findArxivPaperTrace(arxivAuditModel, first.arxivId);
                const verdict =
                  paperTrace && paperTrace.status !== "not_screened"
                    ? paperTrace.status
                    : null;
                const latestCall = item.calls[item.calls.length - 1];
                const latestEnvelope = latestCall ? getEnvelopeForToolCall(latestCall) : undefined;
                const latestArtifact =
                  latestCall?.id
                    ? toolResultsByCallId?.get(latestCall.id)?.artifact ||
                      (latestEnvelope?.artifact_id ? artifactsById?.[latestEnvelope.artifact_id] : undefined)
                    : undefined;
                const timing = getResultTiming(latestEnvelope);
                const duration = formatDuration(aggregateDurationMs(item.calls, getEnvelopeForToolCall));
                const openLatestRead = () => {
                  if (latestArtifact) {
                    onOpenArtifact(
                      paperTrace
                        ? {
                            ...latestArtifact,
                            metadata: {
                              ...(latestArtifact.metadata || {}),
                              arxiv_paper_trace: paperTrace,
                            },
                          }
                        : latestArtifact,
                    );
                    return;
                  }
                  if (!latestEnvelope || !latestCall) return;
                  const meta = isPlainRecord(latestEnvelope.result_metadata)
                    ? latestEnvelope.result_metadata
                    : {};
                  onOpenArtifact({
                    id: latestEnvelope.artifact_id || latestCall.id,
                    type: "data",
                    title: latestCall.name,
                    content:
                      typeof latestEnvelope.result === "string"
                        ? latestEnvelope.result
                        : JSON.stringify(latestEnvelope.result, null, 2),
                    metadata: {
                      tool_name: latestCall.name,
                      tool_call_id: latestCall.id,
                      integration: typeof meta.integration === "string" ? meta.integration : "arxiv",
                      product: typeof meta.product === "string" ? meta.product : "arxiv",
                      ok: latestEnvelope.ok,
                      tool_args: latestCall.args || {},
                      started_at_ms: timing.startedAt,
                      ended_at_ms: timing.endedAt,
                      duration_ms: timing.durationMs,
                      raw_result: latestEnvelope.result,
                      result_metadata: latestEnvelope.result_metadata,
                      ...(paperTrace ? { arxiv_paper_trace: paperTrace } : {}),
                    },
                  });
                };

                const title =
                  last.title ||
                  first.title ||
                  (first.arxivId ? `arXiv:${first.arxivId}` : "arXiv paper");
                const arxivLabel = first.arxivId ? `arXiv:${first.arxivId}` : "arXiv paper";
                const isText = activities.some((activity) => activity.kind === "text");
                const totalChars = Math.max(
                  0,
                  ...activities.map((activity) => activity.totalChars || 0),
                );
                const offsets = activities
                  .map((activity) => activity.offset)
                  .filter((value): value is number => typeof value === "number" && Number.isFinite(value));
                const starts = offsets.length > 0 ? Math.min(...offsets) : undefined;
                const ends = activities
                  .map((activity) =>
                    typeof activity.offset === "number" && typeof activity.charsRead === "number"
                      ? activity.offset + Math.max(0, activity.charsRead)
                      : undefined,
                  )
                  .filter((value): value is number => typeof value === "number" && Number.isFinite(value));
                const end = ends.length > 0 ? Math.max(...ends) : undefined;
                const charsRead = activities.reduce(
                  (sum, activity) => sum + Math.max(0, activity.charsRead || 0),
                  0,
                );
                const startPages = activities
                  .map((activity) => activity.startPage)
                  .filter((value): value is number => typeof value === "number" && Number.isFinite(value));
                const endPages = activities
                  .map((activity) => activity.endPage)
                  .filter((value): value is number => typeof value === "number" && Number.isFinite(value));
                const aggregateInfo: ArxivReadActivity = isText && totalChars > 0
                  ? {
                      ...last,
                      kind: "text",
                      offset: starts ?? last.offset,
                      totalChars,
                      charsRead:
                        starts !== undefined && end !== undefined && end >= starts
                          ? end - starts
                          : charsRead || last.charsRead,
                    }
                  : {
                      ...last,
                      kind: "pages",
                      startPage: startPages.length > 0 ? Math.min(...startPages) : last.startPage,
                      endPage: endPages.length > 0 ? Math.max(...endPages) : last.endPage,
                      pagesRead: activities.reduce(
                        (sum, activity) => sum + Math.max(0, activity.pagesRead || 0),
                        0,
                      ) || last.pagesRead,
                    };
                const sourceKinds = Array.from(
                  new Set(
                    activities
                      .map((activity) => activity.sourceKind.replace(/_/g, " "))
                      .filter(Boolean),
                  ),
                );
                const coverageParts = [
                  `${activities.length.toLocaleString()} read windows`,
                  charsRead > 0 ? `${charsRead.toLocaleString()} chars` : "",
                  totalChars > 0 && end !== undefined
                    ? `through ${Math.round((Math.min(end, totalChars) / totalChars) * 100)}%`
                    : "",
                ].filter(Boolean);

                return (
                  <div
                    key={`arxiv-read-group-${item.originalIndex}`}
                    data-testid="arxiv-read-session-card"
                    className="ta-clean-read-card ta-clean-read-card--settled group relative rounded-md bg-muted/30 transition-colors hover:bg-muted/45 focus-within:ring-2 focus-within:ring-ring"
                  >
                    <div className="flex items-center gap-1.5 pr-2">
                      <button
                        type="button"
                        onClick={openLatestRead}
                        aria-label="Open full audit — arXiv read session"
                        title="Open full audit"
                        className="flex min-w-0 flex-1 items-center gap-2.5 rounded-md px-3 py-2 text-left focus-visible:outline-none after:absolute after:inset-0 after:content-['']"
                      >
                        <BookOpen size={16} className="shrink-0 text-orange-400" aria-hidden />
                        <span className="min-w-0 flex-1">
                          <span className="flex min-w-0 items-center gap-1.5">
                            <span className="block truncate text-xs font-medium text-foreground">
                              {title}
                            </span>
                            <ArxivReadPurposeTag value={last.purposeTag || "READ"} />
                          </span>
                          <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground/85">
                            {arxivLabel} · {coverageParts.join(" · ") || "read session"}
                          </span>
                        </span>
                      </button>
                      <span className="relative flex shrink-0 items-center gap-2">
                        <ArxivStatePill state="read" />
                        {duration && (
                          <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
                            {duration}
                          </span>
                        )}
                      </span>
                    </div>
                    <div className="px-3 pb-2">
                      <ArxivReadRuler info={aggregateInfo} />
                      <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-muted-foreground">
                        {sourceKinds.map((source) => (
                          <span key={source} className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5">
                            {source}
                          </span>
                        ))}
                        {paperTrace && paperTrace.found_by.length > 0 ? (
                          <span className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5">
                            found by {paperTrace.found_by.length === 1 ? "1 query" : `${paperTrace.found_by.length} queries`}
                          </span>
                        ) : null}
                        {verdict ? (
                          <span
                            className={`rounded border px-1.5 py-0.5 ${
                              verdict === "kept"
                                ? "border-emerald-500/25 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
                                : verdict === "possible"
                                  ? "border-amber-500/25 bg-amber-500/10 text-amber-600 dark:text-amber-400"
                                  : "border-border/60 bg-background/35 text-muted-foreground"
                            }`}
                          >
                            verdict: {verdict.replace(/_/g, " ")}
                          </span>
                        ) : null}
                        <span
                          className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 text-amber-500/90"
                          title={last.untrustedNote}
                        >
                          untrusted paper text
                        </span>
                      </div>
                    </div>
                  </div>
                );
              }

              if (item.kind === "arxiv_empty_group" || item.kind === "arxiv_screening_group") {
                const previewKey = `${item.kind}-${item.originalIndex}`;
                const isPreviewOpen = !!openPreviewByKey[previewKey];
                const previewId = `${contentId}-preview-${idx}`;
                const duration = formatDuration(aggregateDurationMs(item.calls, getEnvelopeForToolCall));
                const screening = item.kind === "arxiv_screening_group";
                const getArxivCallArtifact = (call: ToolCall, envelope?: ToolResultEnvelope): Artifact | undefined => {
                  const resultInfo = call.id ? toolResultsByCallId?.get(call.id) : undefined;
                  return resultInfo?.artifact || (envelope?.artifact_id ? artifactsById?.[envelope.artifact_id] : undefined);
                };
                const openArxivCallArtifact = (call: ToolCall, envelope?: ToolResultEnvelope, artifact?: Artifact) => {
                  if (artifact) {
                    onOpenArtifact(artifact);
                    return;
                  }
                  if (!envelope) return;
                  const meta = isPlainRecord(envelope.result_metadata) ? envelope.result_metadata : {};
                  const timing = getResultTiming(envelope);
                  onOpenArtifact({
                    id: envelope.artifact_id || call.id || `${previewKey}-artifact`,
                    type: "data",
                    title: call.name,
                    content: typeof envelope.result === "string" ? envelope.result : JSON.stringify(envelope.result, null, 2),
                    metadata: {
                      tool_name: call.name,
                      tool_call_id: call.id,
                      integration: typeof meta.integration === "string" ? meta.integration : "arxiv",
                      product: typeof meta.product === "string" ? meta.product : "arxiv",
                      ok: envelope.ok,
                      tool_args: call.args || {},
                      started_at_ms: timing.startedAt,
                      ended_at_ms: timing.endedAt,
                      duration_ms: timing.durationMs,
                      raw_result: envelope.result,
                      result_metadata: envelope.result_metadata,
                    },
                  });
                };
                const screeningReports = screening
                  ? item.calls.map((call, callIndex) => {
                      const envelope = getEnvelopeForToolCall(call);
                      const artifact = getArxivCallArtifact(call, envelope);
                      const model = buildArxivRunModelFromEnvelope(call, envelope);
                      const stats = arxivScreeningEvidenceStats(model);
                      const summary = arxivScreeningSummary(model) || "Screened candidate papers";
                      return {
                        call,
                        callIndex,
                        envelope,
                        artifact,
                        model,
                        stats,
                        summary,
                        canOpen: Boolean(artifact || envelope),
                      };
                    })
                  : [];
                const latestReport = screeningReports[screeningReports.length - 1];
                const openArxivRunAudit = () => {
                  if (!arxivAuditModel) {
                    if (latestReport?.canOpen) {
                      openArxivCallArtifact(latestReport.call, latestReport.envelope, latestReport.artifact);
                    }
                    return;
                  }
                  const latestEnvelope = latestReport?.envelope;
                  const latestTiming = getResultTiming(latestEnvelope);
                  onOpenArtifact({
                    id: latestEnvelope?.artifact_id || `${previewKey}-run-audit`,
                    type: "data",
                    title: "arxiv_screening_report",
                    content: JSON.stringify(arxivAuditModel, null, 2),
                    metadata: {
                      tool_name: "arxiv_screening_report",
                      tool_call_id: latestReport?.call.id,
                      integration: "arxiv",
                      product: "arxiv",
                      ok: latestEnvelope?.ok ?? true,
                      tool_args: latestReport?.call.args || {},
                      started_at_ms: latestTiming.startedAt,
                      ended_at_ms: latestTiming.endedAt,
                      duration_ms: latestTiming.durationMs,
                      raw_result: arxivAuditModel,
                      result_metadata: {
                        integration: "arxiv",
                        product: "arxiv",
                        kind: "screening_report",
                        schema: "arxiv_research_audit.v1",
                      },
                    },
                  });
                };
                const title = screening
                  ? `${item.calls.length.toLocaleString()} screening reports`
                  : `${item.calls.length.toLocaleString()} queries found nothing`;
                const subtitle = screening
                  ? `${arxivScreeningSummary(arxivAuditModel) || "Duplicate screening passes"} · view verdicts`
                  : "0 results — starvation, not convergence";
                const groupState = screening ? arxivScreeningState(arxivAuditModel) : "empty";
                return (
                  <div
                    key={`${item.kind}-${item.originalIndex}`}
                    className="group relative rounded-md bg-muted/30 transition-colors focus-within:ring-2 focus-within:ring-ring hover:bg-muted/50"
                  >
                    <div className="flex items-center gap-1.5 pr-2">
                      <button
                        type="button"
                        onClick={() => {
                          if (screening && latestReport?.canOpen) {
                            openArxivRunAudit();
                            return;
                          }
                          togglePreview(previewKey);
                        }}
                        aria-expanded={screening && latestReport?.canOpen ? undefined : isPreviewOpen}
                        aria-controls={screening && latestReport?.canOpen ? undefined : isPreviewOpen ? previewId : undefined}
                        className="flex min-w-0 flex-1 items-center gap-2.5 rounded-md px-3 py-2 text-left focus-visible:outline-none"
                      >
                        <Search size={16} className={screening ? "shrink-0 text-emerald-500" : "shrink-0 text-amber-500"} aria-hidden />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-xs font-medium text-foreground">
                            {title}
                          </span>
                          <span className="block truncate text-xs text-muted-foreground">
                            {subtitle}
                          </span>
                        </span>
                      </button>
                      <span className="relative flex shrink-0 items-center gap-2">
                        <ArxivStatePill state={groupState} />
                        {duration && (
                          <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
                            {duration}
                          </span>
                        )}
                        <button
                          type="button"
                          onClick={() => togglePreview(previewKey)}
                          aria-expanded={isPreviewOpen}
                          aria-controls={isPreviewOpen ? previewId : undefined}
                          aria-label="Toggle grouped arXiv rows"
                          title="Toggle grouped arXiv rows"
                          className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <ChevronRight
                            size={14}
                            aria-hidden
                            className={`transition-transform duration-150 ${isPreviewOpen ? "rotate-90" : ""}`}
                          />
                        </button>
                      </span>
                    </div>
                    {isPreviewOpen && (
                      <div
                        id={previewId}
                        className="motion-safe:animate-in motion-safe:fade-in motion-safe:slide-in-from-top-1 motion-safe:duration-150 border-t border-border/40 px-3 py-2"
                      >
                        <div className="space-y-1.5 text-[11px] text-muted-foreground">
                          {item.calls.map((call, callIndex) => {
                            const trace = arxivTraceForCall(arxivAuditModel, call);
                            const envelope = getEnvelopeForToolCall(call);
                            const report = screeningReports.find((candidate) => candidate.call.id === call.id) || null;
                            const latest = report && latestReport && report.callIndex === latestReport.callIndex;
                            const sameVerdicts = Boolean(
                              report &&
                              latestReport &&
                              !latest &&
                              report.summary === latestReport.summary &&
                              report.stats.kept === latestReport.stats.kept &&
                              report.stats.possible === latestReport.stats.possible &&
                              report.stats.excluded === latestReport.stats.excluded &&
                              report.stats.not_screened === latestReport.stats.not_screened,
                            );
                            const warningCount = report
                              ? report.stats.invalid_evidence + report.stats.validation_warnings
                              : 0;
                            const label = call.name === "arxiv_search"
                              ? arxivPrimaryLabel(call, trace)
                              : `screening report ${callIndex + 1}`;
                            const detail = call.name === "arxiv_search"
                              ? arxivQueryOutcomeLabel(trace)
                              : report?.summary || arxivScreeningSummary(arxivAuditModel);
                            if (report) {
                              return (
                                <button
                                  key={call.id || `${previewKey}-${callIndex}`}
                                  type="button"
                                  disabled={!report.canOpen}
                                  onClick={() => openArxivCallArtifact(call, report.envelope, report.artifact)}
                                  className="block w-full rounded-sm bg-background/45 px-2 py-1.5 text-left transition-colors hover:bg-background/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default disabled:opacity-70"
                                >
                                  <div className="chat-text-wrap font-mono text-foreground/85">
                                    {label}
                                  </div>
                                  <div className="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-muted-foreground/70">
                                    <span>{detail}</span>
                                    <span>{latest ? "latest" : sameVerdicts ? "superseded · same verdicts" : "superseded"}</span>
                                    <span>{report.stats.validated_spans.toLocaleString()} verified spans</span>
                                    {warningCount > 0 ? <span>{warningCount.toLocaleString()} warnings</span> : null}
                                    {arxivEnvelopeCached(envelope) ? <span>cached</span> : null}
                                  </div>
                                </button>
                              );
                            }
                            return (
                              <div key={call.id || `${previewKey}-${callIndex}`} className="rounded-sm bg-background/45 px-2 py-1.5">
                                <div className="chat-text-wrap font-mono text-foreground/85">
                                  {label}
                                </div>
                                <div className="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-muted-foreground/70">
                                  <span>{detail}</span>
                                  {arxivEnvelopeCached(envelope) ? <span>cached</span> : null}
                                </div>
                              </div>
                            );
                          })}
                        </div>
                      </div>
                    )}
                  </div>
                );
              }

              const { tc } = item;
              const isSqlTool = isQueryRunToolName(tc.name);

              if (isSqlTool) {
                const scopedToolKey = getQueryCardStateKey({
                  runId: (tc as any)._uiRunId,
                  messageIdentityKey: getCallMessageIdentityKey(tc),
                  toolCallId: tc.id,
                });
                const resultInfo = scopedToolKey
                  ? sqlToolResultsByScopedKey?.get(scopedToolKey)
                  : undefined;
                const resultArtifact =
                  resultInfo?.artifact ||
                  (resultInfo?.envelope?.artifact_id
                    ? artifactsById?.[resultInfo.envelope.artifact_id]
                    : undefined);
                const hasStartedAt =
                  typeof (tc as any)._uiStartedAt === "number";
                const uiRunId =
                  typeof (tc as any)._uiRunId === "string"
                    ? (tc as any)._uiRunId.trim()
                    : "";
                const progressKey =
                  uiRunId && tc.id ? `${uiRunId}:${tc.id}` : "";
                const isExec =
                  runActive && isLastMessage === true && hasStartedAt && !resultInfo;

                return (
                  <QueryExecutionCard
                    key={idx}
                    toolCall={tc}
                    sqlQuery={
                      typeof tc.args?.sql === "string" ? tc.args.sql : ""
                    }
                    isExecuting={isExec}
                    sseProgress={
                      isExec && progressKey
                        ? toolProgressByKey?.[progressKey]
                        : undefined
                    }
                    toolResultEnvelope={resultInfo?.envelope}
                    artifact={resultArtifact}
                    resultMessageId={resultInfo?.resultMessageId}
                    resultMessageSeq={resultInfo?.resultMessageSeq}
                    resultMessageIdentityKey={
                      resultInfo?.resultMessageIdentityKey
                    }
                    onOpenArtifact={onOpenArtifact}
                    onPinArtifact={onPinArtifact}
                    pinnedArtifactIds={pinnedArtifactIds}
                    parentMessageId={
                      typeof (tc as any)._uiSourceMessageId === "string"
                        ? (tc as any)._uiSourceMessageId
                        : parentMessageId
                    }
                    parentMessageSeq={
                      typeof (tc as any)._uiSourceMessageSeq === "number"
                        ? (tc as any)._uiSourceMessageSeq
                        : parentMessageSeq
                    }
                    toolCallIndex={idx}
                    onToggleMessageReference={onToggleMessageReference}
                    pinnedMessageReferenceIds={pinnedMessageReferenceIds}
                    statusStateKey={scopedToolKey}
                    onStatusChange={handleQueryStatusChange}
                  />
                );
              }

              // Generated-image tool: purpose-built card. Both live transcript
              // paths (collapsed summary + IntermediateThinkingBlock) converge
              // here, so this single branch covers both. Returned as the same
              // single-node shape the generic branch returns so anchoredToolCards
              // wrapping (deep-link scroll/flash) keeps working.
              if (isImageGenerationToolName(tc.name)) {
                const imgResultInfo = tc.id
                  ? toolResultsByCallId?.get(tc.id)
                  : undefined;
                const imgRunId =
                  typeof (tc as any)._uiRunId === "string"
                    ? (tc as any)._uiRunId.trim()
                    : "";
                const imgProgressEvent = findToolProgressForCall(
                  toolProgressByKey,
                  imgRunId,
                  tc.id,
                );
                const imgIsRunning =
                  runActive &&
                  !imgResultInfo &&
                  (isLastMessage === true || Boolean(imgProgressEvent));
                return (
                  <GeneratedImageCard
                    key={idx}
                    toolCall={tc}
                    envelope={imgResultInfo?.envelope}
                    resultArtifact={imgResultInfo?.artifact}
                    artifactsById={artifactsById}
                    ensureArtifactLoaded={ensureArtifactLoaded}
                    progressEvent={imgIsRunning ? imgProgressEvent : undefined}
                    isRunning={imgIsRunning}
                    onOpenArtifact={onOpenArtifact}
                    onPinArtifact={onPinArtifact}
                    pinnedArtifactIds={pinnedArtifactIds}
                    compact={compact}
                  />
                );
              }

              // Non-SQL tool card
              const { icon: Icon, colorClass } = getToolIcon(tc.name);

              const resultInfo = tc.id
                ? toolResultsByCallId?.get(tc.id)
                : undefined;
              const approvalEnvelope = getAtlassianApproval(resultInfo?.envelope)
                ? resultInfo?.envelope
                : undefined;
              const resultArtifact = resultInfo?.artifact;
              const displayTableArtifact =
                tc.name === "display_table"
                  ? buildDisplayTableArtifact(tc, resultInfo)
                  : null;
              const hasStartedAt =
                typeof (tc as any)._uiStartedAt === "number";
              const uiRunId =
                typeof (tc as any)._uiRunId === "string"
                  ? (tc as any)._uiRunId.trim()
                  : "";
              const progressKey =
                uiRunId && tc.id ? `${uiRunId}:${tc.id}` : "";
              const isExec =
                runActive && isLastMessage === true && hasStartedAt && !resultInfo;
              const arxivTrace = arxivTraceForCall(arxivAuditModel, tc);
              const arxivState = arxivRowState(tc, resultInfo?.envelope, arxivTrace, arxivAuditModel, isExec);
              const isArxivTool = isArxivToolName(tc.name);
              const genericProgress =
                isExec && progressKey
                  ? toolProgressByKey?.[progressKey]
                  : undefined;
              const arxivRead = arxivReadActivity(tc, resultInfo?.envelope, genericProgress);
              // Join the read row to its paper's screening trace (verdict,
              // agent rationale, validated evidence spans) so a read card can
              // answer "why was this opened / what did it support / is the
              // evidence verified" from data the run already carries.
              const arxivReadPaper = arxivRead
                ? findArxivPaperTrace(arxivAuditModel, arxivRead.arxivId)
                : null;
              const arxivReadVerdict =
                arxivReadPaper && arxivReadPaper.status !== "not_screened"
                  ? arxivReadPaper.status
                  : null;
              // Register gate (spec: no invented motive). A scan renders
              // scan-notes language and never the findings layer — no
              // evidence chips, no verdict rationale styled as a finding.
              const arxivReadFindings = arxivRead
                ? arxivReadShowsFindings(arxivRead.insight)
                : false;
              const arxivReadLabels = arxivRead
                ? arxivReadRegisterLabels(arxivRead.insight.register)
                : null;
              const arxivReadVerifiedSpans = arxivReadFindings
                ? arxivReadPaper?.evidence?.length ?? 0
                : 0;
              // Source flags use one definition everywhere: failed evidence
              // validation (findings only) + degraded extract + truncated
              // window. The same number labels the detail view's Details
              // expander, so the card's scent matches what opens.
              const arxivReadFlagCount = arxivRead
                ? (arxivReadFindings ? arxivReadPaper?.invalid_evidence?.length ?? 0 : 0)
                  + (arxivRead.extractQuality && !/^(good|ready)$/i.test(arxivRead.extractQuality) ? 1 : 0)
                  + (arxivRead.hasMore ? 1 : 0)
                : 0;
              const arxivReadCountParts = arxivRead
                ? arxivReadDetailCountParts({
                    insight: arxivRead.insight,
                    verifiedEvidenceCount: arxivReadPaper?.evidence?.length ?? 0,
                    flagCount: arxivReadFlagCount,
                  })
                : [];
              // One model sentence at rest, in the read's own register:
              // the takeaway/observation when emitted, the stated purpose
              // while it is all we have, else (findings only) the screening
              // rationale, labeled as the agent judgment it is.
              const arxivReadSentence = arxivRead
                ? arxivRead.insight.takeaway
                  ? { label: arxivReadLabels!.insightLabel, text: compactText(arxivRead.insight.takeaway, 200) }
                  : arxivRead.insight.purpose
                    ? { label: arxivReadLabels!.purposeLabel, text: compactText(arxivRead.insight.purpose, 200) }
                    : arxivReadFindings && arxivReadPaper?.reason?.value
                      ? { label: "Verdict rationale (agent)", text: compactText(arxivReadPaper.reason.value, 200) }
                      : null
                : null;
              const attributionConnectionId = getConnectionForToolName(tc.name);
              const attributionMeta = attributionConnectionId
                ? CONNECTION_META[attributionConnectionId]
                : null;
              const genericProgressPercent =
                getProgressPercent(genericProgress);
              const genericProgressDetails =
                getGenericProgressDetails(genericProgress);
              const rowHasError = isToolResultFailure(resultInfo?.envelope);
              const rowCancelled = isEnvelopeCancelled(resultInfo?.envelope);
              const rowErrorDetail = envelopeErrorCardText(resultInfo?.envelope);
              const rowDuration = formatDuration(
                getResultTiming(resultInfo?.envelope).durationMs,
              );
              const cardStatus: InspectorStatusKind = isExec
                ? "running"
                : rowHasError
                  ? "error"
                  : rowCancelled
                    ? "cancelled"
                    : "success";
              // Subtitle: live progress while running; result summary (or a
              // human-readable fallback) once complete.
              const subtitle = isExec
                ? arxivRead
                  ? arxivReadSubtitle(arxivRead, true)
                  : compactText(genericProgress?.label, 90) ||
                    getGenericProgressLabel(genericProgress)
                : isArxivTool
                  ? arxivRead
                    ? arxivReadSubtitle(arxivRead, false)
                    : arxivSubtitle(tc, arxivTrace, arxivAuditModel, resultInfo?.envelope)
                : getToolResultSummary(tc, resultInfo?.envelope) ||
                  getArgsPreview(tc) ||
                  getActivityLabel(tc);
              const arxivReadBeat = arxivRead
                ? arxivReadLifecycleBeat(isExec, genericProgress)
                : "settled";
              const arxivReadProgressLabel = arxivRead
                ? arxivReadLifecycleLabel(
                    arxivReadBeat,
                    genericProgress,
                    genericProgressDetails || subtitle,
                  )
                : "";
              const primaryLabel = isArxivTool
                ? arxivRead
                  ? arxivReadPrimaryLabel(arxivRead, isExec)
                  : arxivPrimaryLabel(tc, arxivTrace)
                : tc.name;
              const RowIcon = arxivRead ? BookOpen : Icon;
              const rowIconClass = arxivRead ? "text-orange-400" : colorClass;
              const PreviewComponent = resolveInlineToolPreview(tc, resultInfo?.envelope);
              const previewProps: ToolInlinePreviewProps = {
                toolCall: tc,
                envelope: resultInfo?.envelope,
                artifact: resultArtifact || displayTableArtifact || undefined,
              };
              const previewKey = tc.id || `idx-${idx}`;
              const isPreviewOpen = !!openPreviewByKey[previewKey];
              const previewId = `${contentId}-preview-${idx}`;

              // The inspector only receives the artifact, so the read detail
              // view cannot re-derive the group-scope screening join (verdict,
              // rationale, validated evidence). Stamp the joined paper trace
              // onto whatever artifact gets opened.
              const withArxivReadTrace = (artifact: Artifact): Artifact =>
                arxivRead && arxivReadPaper
                  ? {
                      ...artifact,
                      metadata: {
                        ...(artifact.metadata || {}),
                        arxiv_paper_trace: arxivReadPaper,
                      },
                    }
                  : artifact;

              // For display_table, we can construct the artifact from args even without a result message
              const handleOpenAudit = () => {
                if (resultArtifact) {
                  onOpenArtifact(withArxivReadTrace(resultArtifact));
                } else if (displayTableArtifact) {
                  onOpenArtifact(displayTableArtifact);
                } else if (resultInfo?.envelope) {
                  // Construct an artifact carrying enough context for the
                  // tool-specific inspector renderer (see GitHubToolRenderer).
                  const env = resultInfo.envelope;
                  const meta = isPlainRecord(env.result_metadata)
                    ? env.result_metadata
                    : {};
                  const timing = getResultTiming(env);
                  const artifact: Artifact = {
                    id: env.artifact_id || tc.id,
                    type: "data",
                    title: tc.name,
                    content:
                      typeof env.result === "string"
                        ? env.result
                        : JSON.stringify(env.result, null, 2),
                    metadata: {
                      tool_name: tc.name,
                      tool_call_id: tc.id,
                      integration:
                        typeof meta.integration === "string"
                          ? meta.integration
                          : attributionConnectionId || undefined,
                      product:
                        typeof meta.product === "string" ? meta.product : undefined,
                      widget:
                        typeof meta.widget === "string" ? meta.widget : undefined,
                      ok: env.ok,
                      tool_args: tc.args || {},
                      started_at_ms: timing.startedAt,
                      ended_at_ms: timing.endedAt,
                      duration_ms: timing.durationMs,
                      raw_result: env.result,
                      result_metadata: env.result_metadata,
                    },
                  };
                  onOpenArtifact(withArxivReadTrace(artifact));
                }
              };

              const canOpenAudit = !!(
                resultArtifact ||
                displayTableArtifact ||
                (resultInfo?.envelope && !approvalEnvelope)
              );

              const bodyOpensAudit = canOpenAudit;

              return (
                <React.Fragment key={tc.id || `idx-${item.originalIndex}`}>
                  {/* The whole card is the click target (opens the full audit
                      overlay) and the whole surface highlights on hover/focus.
                      The chevron is the explicit secondary affordance for the
                      inline preview; while no result exists yet the body falls
                      back to that preview. */}
                  <div
                    data-testid={arxivRead ? "arxiv-clean-read-card" : undefined}
                    data-state={arxivRead ? (isExec ? "running" : "settled") : undefined}
                    data-beat={arxivRead ? arxivReadBeat : undefined}
                    className={`group relative transition-colors focus-within:ring-2 ${
                      rowHasError
                        ? "rounded-md bg-destructive/5 ring-1 ring-inset ring-destructive/40 hover:bg-destructive/10 focus-within:ring-ring"
                        : arxivRead
                          ? `ta-clean-read-card rounded-md ${
                              isExec
                                ? "ta-clean-read-card--running bg-orange-500/5 hover:bg-orange-500/10 focus-within:ring-orange-500/35"
                                : "ta-clean-read-card--settled bg-muted/30 hover:bg-muted/45 focus-within:ring-ring"
                            }`
                        : arxivState === "empty"
                          ? "rounded-md bg-amber-500/5 ring-1 ring-inset ring-amber-500/15 hover:bg-amber-500/10 focus-within:ring-ring"
                        : "rounded-md bg-muted/40 hover:bg-muted/60 focus-within:ring-ring"
                    }`}
                  >
                    <div className="flex items-center gap-1.5 pr-2">
                      <button
                        type="button"
                        onClick={bodyOpensAudit ? handleOpenAudit : () => togglePreview(previewKey)}
                        aria-label={
                          bodyOpensAudit ? `Open full audit — ${tc.name}` : undefined
                        }
                        title={bodyOpensAudit ? "Open full audit" : undefined}
                        aria-expanded={bodyOpensAudit ? undefined : isPreviewOpen}
                        aria-controls={
                          !bodyOpensAudit && isPreviewOpen ? previewId : undefined
                        }
                        className={`flex min-w-0 flex-1 items-center gap-2.5 rounded-md px-3 py-2 text-left focus-visible:outline-none${
                          bodyOpensAudit && !isPreviewOpen
                            ? " after:absolute after:inset-0 after:content-['']"
                            : ""
                        }`}
                      >
                        {attributionMeta ? (
                          <span
                            className={`ta-scope ta-rowlogo${attributionMeta.mono ? " ta-mono" : ""}`}
                            aria-hidden="true"
                            title={attributionMeta.label}
                          >
                            <ConnectionLogo meta={attributionMeta} />
                          </span>
                        ) : (
                          <RowIcon size={16} className={`${rowIconClass} shrink-0`} aria-hidden />
                        )}
                        {arxivRead ? (
                          <span className="min-w-0 flex-1">
                            <span className="flex min-w-0 items-center gap-1.5">
                              <span className="block truncate text-xs font-medium text-foreground">
                                {primaryLabel}
                              </span>
                              <ArxivReadPurposeTag value={arxivRead.purposeTag} />
                            </span>
                            {arxivRead.insight.paper_gloss && (
                              <span
                                className="mt-0.5 block truncate text-[11px] text-muted-foreground/85"
                                title={arxivRead.insight.paper_gloss}
                              >
                                {compactText(arxivRead.insight.paper_gloss, 160)}
                              </span>
                            )}
                            <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground/85">
                              {arxivReadLocator(arxivRead)}
                            </span>
                            {subtitle && (
                              <span
                                className={`mt-0.5 block truncate text-xs ${
                                  rowHasError ? "text-red-400" : "text-muted-foreground"
                                }`}
                              >
                                {subtitle}
                              </span>
                            )}
                          </span>
                        ) : (
                          <span className="min-w-0 flex-1">
                            <span className="block truncate font-mono text-xs text-foreground">
                              {primaryLabel}
                            </span>
                            {subtitle && (
                              <span
                                className={`block truncate text-xs ${
                                  rowHasError ? "text-red-400" : "text-muted-foreground"
                                }`}
                              >
                                {subtitle}
                              </span>
                            )}
                          </span>
                        )}
                      </button>
                      <span className="relative flex shrink-0 items-center gap-2">
                        {arxivRead && isExec && !rowHasError && !rowCancelled ? (
                          <span className="inline-flex items-center gap-1 rounded-md border border-orange-500/25 bg-orange-500/10 px-2 py-0.5 font-mono text-[11px] font-medium text-orange-500 dark:text-orange-400">
                            {genericProgressPercent !== null
                              ? `${genericProgressPercent}%`
                              : "reading"}
                          </span>
                        ) : arxivState && !rowHasError && !rowCancelled ? (
                          <ArxivStatePill state={arxivState} />
                        ) : (isExec || resultInfo) && (
                          <StatusPill status={cardStatus} subject={tc.name} />
                        )}
                        {!isExec && rowDuration && (
                          <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
                            {rowDuration}
                          </span>
                        )}
                        <button
                          type="button"
                          onClick={() => togglePreview(previewKey)}
                          aria-expanded={isPreviewOpen}
                          aria-controls={isPreviewOpen ? previewId : undefined}
                          aria-label="Toggle inline preview"
                          title="Toggle inline preview"
                          className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <ChevronRight
                            size={14}
                            aria-hidden
                            className={`transition-transform duration-150 ${isPreviewOpen ? "rotate-90" : ""}`}
                          />
                        </button>
                      </span>
                    </div>
                    {arxivRead && (
                      <div className="px-3 pb-2">
                        <ArxivReadRuler
                          info={arxivRead}
                          running={isExec}
                          progressPercent={genericProgressPercent}
                          ariaValueText={
                            isExec
                              ? genericProgressDetails || arxivReadProgressLabel || undefined
                              : undefined
                          }
                        />
                        {isExec ? (
                          <div className="ta-clean-read-live">
                            {arxivRead.insight.purpose ? (
                              <div className="ta-clean-read-purpose">
                                <span>{arxivReadLabels?.purposeLabel || "Looking for"}: </span>
                                {compactText(arxivRead.insight.purpose, 180)}
                              </div>
                            ) : null}
                            <ArxivReadInterstitial
                              label={arxivReadProgressLabel}
                              percent={genericProgressPercent}
                            />
                            <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-muted-foreground">
                              {arxivRead.extractQuality ? (
                                <span className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5">
                                  {arxivRead.extractQuality.replace(/_/g, " ")}
                                </span>
                              ) : null}
                              {arxivRead.sourceKind ? (
                                <span className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5">
                                  {arxivRead.sourceKind.replace(/_/g, " ")}
                                </span>
                              ) : null}
                              {arxivRead.cached ? (
                                <span className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5">
                                  local cache
                                </span>
                              ) : null}
                              <span
                                className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 text-amber-500/90"
                                title={arxivRead.untrustedNote}
                              >
                                untrusted paper text
                              </span>
                            </div>
                          </div>
                        ) : (
                          <>
                            <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-muted-foreground">
                              {arxivRead.extractQuality ? (
                                <span className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5">
                                  {arxivRead.extractQuality.replace(/_/g, " ")}
                                </span>
                              ) : null}
                              {arxivRead.sourceKind ? (
                                <span className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5">
                                  {arxivRead.sourceKind.replace(/_/g, " ")}
                                </span>
                              ) : null}
                              {arxivRead.cached ? (
                                <span className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5">
                                  local cache
                                </span>
                              ) : null}
                              {arxivRead.hasMore ? (
                                <span
                                  className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5"
                                  title="The window stopped before the end of the paper"
                                >
                                  more pages remain{arxivRead.nextPage !== undefined ? ` · next p. ${arxivRead.nextPage}` : ""}
                                </span>
                              ) : null}
                              {arxivReadPaper && arxivReadPaper.found_by.length > 0 ? (
                                <span
                                  className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5"
                                  title="Deterministic query provenance from this run"
                                >
                                  found by {arxivReadPaper.found_by.length === 1 ? "1 query" : `${arxivReadPaper.found_by.length} queries`}
                                </span>
                              ) : null}
                              {arxivReadVerdict ? (
                                <span
                                  className={`rounded border px-1.5 py-0.5 ${
                                    arxivReadVerdict === "kept"
                                      ? "border-emerald-500/25 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
                                      : arxivReadVerdict === "possible"
                                        ? "border-amber-500/25 bg-amber-500/10 text-amber-600 dark:text-amber-400"
                                        : "border-border/60 bg-background/35 text-muted-foreground"
                                  }`}
                                  title="Agent screening judgment from this run's report"
                                >
                                  verdict: {arxivReadVerdict.replace(/_/g, " ")}
                                </span>
                              ) : null}
                              {arxivReadVerifiedSpans > 0 ? (
                                <span
                                  className="inline-flex items-center gap-1 rounded border border-emerald-500/25 bg-emerald-500/10 px-1.5 py-0.5 text-emerald-600 dark:text-emerald-400"
                                  title="Evidence quotes located by the deterministic validator"
                                >
                                  <CheckCircle2 size={10} aria-hidden />
                                  {arxivReadVerifiedSpans === 1 ? "1 span verified" : `${arxivReadVerifiedSpans} spans verified`}
                                </span>
                              ) : null}
                              {arxivReadFindings && arxivRead.insight.captured_span_count > 0 ? (
                                <span
                                  className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 text-amber-500/90"
                                  title="Captured by the model; not yet located by the validator"
                                >
                                  {arxivRead.insight.captured_span_count === 1
                                    ? "1 span captured · unverified"
                                    : `${arxivRead.insight.captured_span_count} spans captured · unverified`}
                                </span>
                              ) : null}
                              {!arxivReadFindings && arxivRead.insight.register_explicit ? (
                                <span
                                  className="rounded border border-border/60 bg-background/35 px-1.5 py-0.5"
                                  title="Scan register: low-commitment observations, not findings"
                                >
                                  observations, not conclusions
                                </span>
                              ) : null}
                              <span
                                className="rounded border border-amber-500/25 bg-amber-500/10 px-1.5 py-0.5 text-amber-500/90"
                                title={arxivRead.untrustedNote}
                              >
                                untrusted paper text
                              </span>
                            </div>
                            {arxivReadSentence && (
                              <div className="ta-clean-read-insight mt-2 rounded-md border border-border/45 bg-background/35 px-2 py-1.5 text-[11px] leading-snug text-muted-foreground">
                                <p className="chat-text-wrap">
                                  <span className="font-medium text-foreground/75">{arxivReadSentence.label}: </span>
                                  {arxivReadSentence.text}
                                </p>
                              </div>
                            )}
                            {arxivReadCountParts.length > 0 && (
                              <div
                                className="ta-clean-read-details mt-1.5 font-mono text-[10px] text-muted-foreground/70"
                                title="Indexed in the full audit — open the card for the contents"
                              >
                                Details — {arxivReadCountParts.join(" · ")}
                              </div>
                            )}
                          </>
                        )}
                      </div>
                    )}
                    {isExec && !arxivRead && (
                      <div className="px-3 pb-2">
                        <div
                          className="h-0.5 w-full overflow-hidden rounded-full bg-border"
                          role="progressbar"
                          aria-label={isArxivTool ? "Paper reading progress" : "Tool progress"}
                          aria-valuemin={0}
                          aria-valuemax={100}
                          aria-valuenow={genericProgressPercent ?? undefined}
                          aria-valuetext={genericProgressDetails || subtitle || undefined}
                        >
                          {genericProgressPercent !== null ? (
                            <div
                              className="h-full rounded-full bg-muted-foreground/50 transition-all duration-500 ease-out"
                              style={{ width: `${genericProgressPercent}%` }}
                            />
                          ) : (
                            <div className="h-full w-1/3 rounded-full bg-muted-foreground/50 motion-safe:animate-[shimmer_1.5s_ease-in-out_infinite]" />
                          )}
                        </div>
                        {genericProgressDetails && (
                          <div className="mt-1 truncate text-[10px] text-muted-foreground">
                            {genericProgressDetails}
                          </div>
                        )}
                      </div>
                    )}
                    {isPreviewOpen && (
                      <div
                        id={previewId}
                        className="motion-safe:animate-in motion-safe:fade-in motion-safe:slide-in-from-top-1 motion-safe:duration-150 border-t border-border/40 px-3 py-2"
                      >
                        <PreviewComponent {...previewProps} />
                      </div>
                    )}
                    {rowHasError && rowErrorDetail && (
                      <div className="relative mx-3 mb-2 min-w-0 rounded-md border border-destructive/20 bg-destructive/5 px-2 py-1.5">
                        <p className="chat-text-wrap text-[11px] leading-snug text-red-400">
                          {rowErrorDetail}
                        </p>
                      </div>
                    )}
                  </div>
                  {approvalEnvelope && (
                    <AtlassianApprovalCard envelope={approvalEnvelope} />
                  )}
                </React.Fragment>
              );
            });

  // Wrap every card in a per-call DOM anchor so provenance deep links
  // ("open origin chat" in the files audit) can scroll to and flash the exact
  // tool event. Grouped cards carry all of their member call ids.
  const renderAnchoredToolCard = (
    item: (typeof toolCardItems)[number],
    idx: number,
  ) => {
    const card = toolCards[idx];
    if (!card) return null;
    const anchorIds = (item.kind === "tool" ? [item.tc] : item.calls)
      .map((call) => String(call?.id || "").trim())
      .filter(Boolean);
    const isRevealed = !!revealFlash && anchorIds.includes(revealFlash.callId);
    return (
      <div
        key={anchorIds[0] || `tool-card-anchor-${idx}`}
        data-tool-call-anchor={anchorIds.join(" ") || undefined}
        tabIndex={-1}
        className={`rounded-lg focus:outline-none${isRevealed ? " ta-reveal-flash" : ""}`}
      >
        {card}
      </div>
    );
  };

  // Generated images are primary chat output, not diagnostic detail. Keep
  // their rich card visible even after the tool-audit group auto-collapses;
  // the audit pill still carries progress, approvals, timing, and disclosure.
  const isGeneratedImageItem = (item: (typeof toolCardItems)[number]) =>
    item.kind === "tool" && isImageGenerationToolName(item.tc.name);
  const anchoredToolCards = toolCardItems.map((item, idx) =>
    isGeneratedImageItem(item) ? null : renderAnchoredToolCard(item, idx),
  );
  const surfacedGeneratedImageCards = toolCardItems.map((item, idx) =>
    isGeneratedImageItem(item) ? renderAnchoredToolCard(item, idx) : null,
  );

  /* ----- View assembly: minimized pill / expanded container / compact ----- */

  let pillSecondary: React.ReactNode = null;
  if (failedCount > 0) {
    pillSecondary = (
      <span className="text-xs text-muted-foreground">
        <span className="text-red-400">{failedSummaryText}</span>
        {groupDuration ? ` · ${groupDuration}` : ""}
      </span>
    );
  } else if (cancelledCount > 0) {
    pillSecondary = (
      <span className="text-xs text-muted-foreground">
        {`${cancelledCount} cancelled`}
        {groupDuration ? ` · ${groupDuration}` : ""}
      </span>
    );
  } else if (doneCount === totalCount) {
    pillSecondary = (
      <span className="text-xs text-muted-foreground">
        {totalCount === 1 ? "succeeded" : "all succeeded"}
        {groupDuration ? ` · ${groupDuration}` : ""}
      </span>
    );
  }

  // Neutral dot when nothing completed (e.g. a stopped run) — a green
  // "success" claim would be dishonest.
  const pillDotClass =
    failedCount > 0
      ? "bg-red-500"
      : cancelledCount > 0 || doneCount !== totalCount
        ? "bg-muted-foreground"
        : "bg-emerald-500";

  const minimizedPill = (
    <button
      ref={pillRef}
      type="button"
      onClick={() => setOpenWithFocusRestore(true)}
      aria-expanded={false}
      // Same width as the expanded container so the pill <-> expanded swap
      // doesn't jump horizontally; content stays left-grouped and concise,
      // the chevron anchors to the right edge (mirrors the design mockup).
      className="motion-safe:animate-in motion-safe:fade-in motion-safe:duration-200 flex w-full items-center gap-2 rounded-lg border border-border bg-muted/20 px-3 py-1.5 text-left transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span className={`h-2 w-2 shrink-0 rounded-full ${pillDotClass}`} aria-hidden="true" />
      {logoStack}
      <span className="min-w-0 truncate text-[13px] font-medium text-foreground">
        {groupTitle}
      </span>
      {pillSecondary && <span className="min-w-0 truncate">{pillSecondary}</span>}
      <ChevronRight size={14} className="ml-auto shrink-0 text-muted-foreground" aria-hidden />
    </button>
  );

  const expandedView = (
    <div className="motion-safe:animate-in motion-safe:fade-in motion-safe:slide-in-from-top-1 motion-safe:duration-200 rounded-lg border border-border bg-background/40 p-2">
      {isPinned ? (
        <div className="flex items-center gap-2 px-1 py-0.5">
          <Loader2 size={14} className="shrink-0 motion-safe:animate-spin text-blue-400" aria-hidden />
          <span className="text-[13px] font-medium text-foreground">
            {arxivWorkingLabel}
          </span>
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {`${doneCount} of ${totalCount} done`}
          </span>
        </div>
      ) : (
        <div className="flex items-center gap-1">
          <button
            ref={headerToggleRef}
            type="button"
            onClick={() => setOpenWithFocusRestore(false)}
            aria-expanded={true}
            aria-controls={contentId}
            className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-1 py-0.5 text-left transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <ChevronRight
              size={14}
              className="shrink-0 rotate-90 text-muted-foreground transition-transform"
              aria-hidden
            />
            <span className="min-w-0 truncate text-[13px] font-medium text-foreground">
              {groupTitle}
            </span>
            <span className="truncate font-mono text-xs tabular-nums text-muted-foreground">
              {failedCount > 0 && (
                <span className="text-red-400">{failedSummaryText}</span>
              )}
              {failedCount > 0 && groupDuration ? " · " : ""}
              {groupDuration}
            </span>
          </button>
        </div>
      )}
      {arxivClockLine && (
        <div className="mt-1 px-7 text-[11px] text-muted-foreground">
          {arxivClockLine}
        </div>
      )}
      <div id={contentId} className="mt-2 space-y-2">
        {anchoredToolCards}
      </div>
    </div>
  );

  const compactView = (
    <div>
      {isPinned ? (
        <div className="flex w-full items-center gap-2 rounded-md px-2 py-1.5">
          <Loader2 size={12} className="shrink-0 motion-safe:animate-spin text-blue-400" aria-hidden />
          {logoStack}
          <span className="sr-only">Working</span>
        </div>
      ) : (
        <button
          ref={pillRef}
          type="button"
          onClick={() => setOpenWithFocusRestore(!isOpen)}
          aria-expanded={isOpen}
          aria-controls={isOpen ? contentId : undefined}
          aria-label={`Tool activity: ${groupTitle}${
            failedCount > 0 ? `, ${failedSummaryText}` : ""
          }`}
          className="flex w-full cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ChevronRight
            size={12}
            className={`text-muted-foreground/60 transition-transform duration-200 ${isOpen ? "rotate-90" : ""}`}
            aria-hidden
          />
          {logoStack}
          {failedCount > 0 && (
            <>
              <span className="h-2 w-2 shrink-0 rounded-full bg-red-500" aria-hidden="true" />
              <span className="truncate text-[11px] font-medium text-red-400">
                {failedSummaryText}
              </span>
            </>
          )}
        </button>
      )}
      {isOpen && (
        <div id={contentId} className="mt-0.5 space-y-1.5">
          {anchoredToolCards}
        </div>
      )}
    </div>
  );

  return (
    <div
      ref={containerRef}
      className="my-2 space-y-1.5"
      role="group"
      aria-label="Tool activity"
      aria-busy={isPinned || undefined}
      onMouseEnter={() => {
        pointerInsideRef.current = true;
      }}
      onMouseLeave={() => {
        pointerInsideRef.current = false;
      }}
    >
      {compact ? compactView : isOpen ? expandedView : minimizedPill}
      {surfacedGeneratedImageCards.some(Boolean) && (
        <div className="mt-2 space-y-2">{surfacedGeneratedImageCards}</div>
      )}
      <span role="status" aria-live="polite" className="sr-only">
        {announcement}
      </span>

      {/* Inline chart preview cards — for any tool that produced a chart (suppressed in compact/thinking-stack mode) */}
      {!compact &&
        chartArtifacts.map(
          ({ key, artifact, plotSpec, chartEngine, title, dataJson }) => (
            <ArtifactCard
              key={key}
              testId="tool-chart-artifact-card"
              title={title}
              type="chart"
              details={buildChartDetails(chartEngine, plotSpec, dataJson)}
              plotSpec={plotSpec}
              onClick={() => onOpenArtifact(artifact)}
              onDownload={
                canDownloadArtifact(artifact) &&
                artifact.metadata?.partial !== true &&
                artifact.metadata?.preview_only !== true
                  ? () => downloadArtifact(artifact)
                  : undefined
              }
              onPin={
                onPinArtifact ? (_e) => onPinArtifact(artifact) : undefined
              }
              isPinned={pinnedArtifactIds?.includes(artifact.id)}
            />
          ),
        )}
      {!compact &&
        htmlArtifacts.map(({ key, artifact: sourceArtifact, title }, index) => {
          const artifact =
            hydratedHtmlArtifactsById[sourceArtifact.id] || sourceArtifact;
          return (
            <HtmlArtifactCard
              key={key}
              testId="sandboxed-html-artifact-card"
              artifact={artifact}
              title={title}
              showThumbnail={index < INLINE_HTML_PREVIEW_LIMIT}
              onOpenArtifact={onOpenArtifact}
              onDownload={
                canDownloadArtifact(artifact)
                  ? () => downloadArtifact(artifact)
                  : undefined
              }
              onPin={onPinArtifact}
              isPinned={pinnedArtifactIds?.includes(artifact.id)}
            />
          );
        })}
      {!compact &&
        generatedFileArtifacts.map(({ key, artifact }) => {
          const presentation = fileArtifactCardPresentation(artifact);
          const rawDownload = isRawDownloadArtifact(artifact);
          const contentDownload = !rawDownload && canDownloadArtifact(artifact);
          return (
            <FileAttachmentCard
              key={key}
              title={artifact.title || artifact.id}
              mimeType={presentation.mimeType}
              fileKind={presentation.fileKind}
              sizeBytes={presentation.sizeBytes}
              previewText={presentation.previewText}
              contentNote={presentation.contentNote}
              previewAvailable={presentation.previewAvailable}
              previewTruncated={presentation.previewTruncated}
              metadata={presentation.metadata}
              thumbnailUrl={presentation.thumbnailUrl}
              thumbnailFetchUrl={presentation.thumbnailFetchUrl}
              thumbnailArtifactId={presentation.thumbnailArtifactId}
              pdfPreviewUrl={presentation.pdfPreviewUrl}
              htmlPreviewUrl={presentation.htmlPreviewUrl}
              htmlPreviewContent={presentation.htmlPreviewContent}
              textPreviewFetchUrl={presentation.textPreviewFetchUrl}
              downloadArtifactId={rawDownload ? artifact.id : undefined}
              downloadFileName={rawDownload ? artifact.title || artifact.id : undefined}
              onDownload={contentDownload ? () => downloadArtifact(artifact) : undefined}
              onClick={() => onOpenArtifact(artifact)}
              className="max-w-full sm:max-w-[28rem]"
            />
          );
        })}
    </div>
  );
};

export default CollapsedToolsSummary;
