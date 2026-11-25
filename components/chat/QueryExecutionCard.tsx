import React, { useEffect, useId, useMemo, useState } from 'react';
import { ChevronRight, Database } from 'lucide-react';
import { cn } from '@/lib/utils';
import ConnectionLogo from '@/components/chat/ConnectionLogo';
import MessageReferenceButton from './MessageReferenceButton';
import {
  StatusPill,
  formatDurationMs,
  formatElapsedColon,
  shortId,
  statusKindFor,
} from '@/components/renderers/tool/query/atoms';
import { useQueryInspectorState } from '@/components/renderers/tool/query/useQueryInspectorState';
import { buildArtifactFromEnvelope } from '@/hooks/useChat';
import type {
  Artifact,
  MessageBlockReference,
  QueryJobStatus,
  ToolCall,
  ToolProgressEvent,
  ToolResultEnvelope,
} from '@/types';
import { Role } from '@/types';
import { hasNonCollapsedSelectionWithin } from '@/lib/textSelection';
import {
  extractToolErrorDiagnosticFromEnvelope,
  formatToolErrorCardText,
} from '@/lib/toolErrorDiagnostics';

/**
 * QueryExecutionCard — compact chat summary for a query tool call.
 *
 * The full three-zone QueryInspector no longer renders inline in chat; this
 * card is a one-row status bar (provider logo · status pill · description ·
 * elapsed/percent/rows/job chip · progress underline while running) whose
 * primary action — clicking anywhere on the card — opens the full-window
 * audit overlay, where the full inspector / data explorer lives. The inline
 * SQL preview is a secondary affordance behind the explicit chevron toggle.
 *
 * Open-target resolution, in order:
 *   1. The materialised artifact passed by the call site (placeholder while
 *      the async job runs, replaced with the loaded explorer artifact later).
 *   2. An artifact derived from the tool result envelope via the same
 *      buildArtifactFromEnvelope used by useChat — covers the moment the
 *      async handle exists but the artifact map hasn't caught up yet, and
 *      keeps the derived id equal to the eventual artifact id.
 *   3. Pre-submit executing phase (no envelope yet) — a derived submitting
 *      placeholder keyed `pending-query:<tool call>` so the panel still opens
 *      immediately; App swaps the open snapshot to the real artifact by
 *      tool_call_id as soon as one materialises.
 *
 * All state derivation (phase machine, polling, branding, stats) stays in
 * useQueryInspectorState, shared with the side-panel inspector, so the two
 * surfaces can never disagree and no provider logic lives here.
 */

interface QueryExecutionCardProps {
  toolCall: ToolCall;
  /** Kept for backwards compatibility with the call site; ignored because
      the inspector state reads SQL from the envelope/args itself. */
  sqlQuery: string;
  isExecuting: boolean;
  sseProgress?: ToolProgressEvent;
  toolResultEnvelope?: ToolResultEnvelope;
  artifact?: Artifact;
  resultMessageId?: string;
  resultMessageSeq?: number;
  resultMessageIdentityKey?: string;
  /** Opens the full-window audit overlay — the card's primary action. */
  onOpenArtifact?: (a: Artifact) => void;
  /** Not used by the compact card — pinning happens via message reference. */
  onPinArtifact?: (artifact: Artifact) => void;
  pinnedArtifactIds?: string[];
  parentMessageId?: string;
  parentMessageSeq?: number;
  toolCallIndex?: number;
  onToggleMessageReference?: (reference: MessageBlockReference) => void;
  pinnedMessageReferenceIds?: string[];
  statusStateKey?: string;
  onStatusChange?: (key: string, status: QueryJobStatus) => void;
}

function normalizeSnapshot(value: string): string {
  return String(value || '')
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .trim();
}

function toPreview(value: string, maxChars = 180): string {
  const oneLine = value.replace(/\s+/g, ' ').trim();
  if (!oneLine) return '(empty)';
  if (oneLine.length <= maxChars) return oneLine;
  return `${oneLine.slice(0, maxChars - 3).trimEnd()}...`;
}

function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value ?? '');
  }
}

const QueryExecutionCard: React.FC<QueryExecutionCardProps> = ({
  toolCall,
  isExecuting,
  sseProgress,
  toolResultEnvelope,
  artifact,
  resultMessageId,
  resultMessageSeq,
  resultMessageIdentityKey,
  onOpenArtifact,
  parentMessageId,
  parentMessageSeq,
  toolCallIndex,
  onToggleMessageReference,
  pinnedMessageReferenceIds,
  statusStateKey,
  onStatusChange,
}) => {
  const state = useQueryInspectorState({
    artifact,
    toolCall,
    toolResultEnvelope,
    sseProgress,
    isExecuting,
  });
  const {
    phase,
    status,
    provider,
    providerLabel,
    brand,
    connectorMeta,
    description,
    sql,
    stats,
    running,
    errorContext,
    startedAtMs,
  } = state;

  useEffect(() => {
    if (!statusStateKey || !onStatusChange) return;
    onStatusChange(statusStateKey, status);
  }, [onStatusChange, status, statusStateKey]);

  /* ---------- Open target ---------- */

  const openTarget = useMemo<Artifact | null>(() => {
    if (artifact) return artifact;
    if (toolResultEnvelope) {
      return buildArtifactFromEnvelope(toolResultEnvelope, toolCall.name, null);
    }
    // Pre-submit executing phase: no envelope yet, so derive a submitting
    // placeholder keyed to the tool call. Opening it shows the running
    // inspector immediately; App upgrades the open snapshot by tool_call_id
    // once the async handle / result artifact materialises under its real id.
    const callIdentity = String(toolCall.id || `index:${toolCallIndex ?? 0}`).trim();
    return {
      id: `pending-query:${callIdentity}`,
      type: 'data',
      title: 'Query Running',
      content: '',
      metadata: {
        tool_name: toolCall.name,
        ...(toolCall.id ? { tool_call_id: toolCall.id } : {}),
        ok: true,
        placeholder: true,
        async_query: true,
        pre_submit: true,
        provider,
        ...(sql ? { sql } : {}),
        ...(description ? { description } : {}),
        started_at_ms: startedAtMs,
        _uiStartedAt: startedAtMs,
        query_metadata: {
          provider,
          status: 'submitted',
          ...(sql ? { sql } : {}),
          ...(description ? { description } : {}),
        },
      },
    } as Artifact;
  }, [
    artifact,
    toolResultEnvelope,
    toolCall.id,
    toolCall.name,
    toolCallIndex,
    provider,
    sql,
    description,
    startedAtMs,
  ]);

  const canOpen = Boolean(openTarget && onOpenArtifact);

  /* ---------- Inline preview (explicit chevron toggle) ---------- */

  // Clicking anywhere on the card opens the full-window audit overlay — a
  // pre-submit placeholder target exists even while the query is still
  // running, so the click always lands. The chevron on the right is the
  // explicit toggle for this small inline SQL preview. Without an
  // onOpenArtifact handler the body degrades to the preview toggle instead
  // of advertising an open action it cannot perform.
  const [isPreviewOpen, setIsPreviewOpen] = useState(false);
  const previewId = useId();

  /* ---------- Row content ---------- */

  const sqlOneLine = useMemo(() => sql.replace(/\s+/g, ' ').trim(), [sql]);
  const summaryText =
    phase === 'failed'
      ? errorContext.errorMessage || description || sqlOneLine || 'Query failed'
      : description || sqlOneLine || `${providerLabel} query`;
  const errorDetail = phase === 'failed'
    ? formatToolErrorCardText(
        extractToolErrorDiagnosticFromEnvelope(toolResultEnvelope, {
          fallbackTitle: toolCall.name,
        }),
      )
    : null;

  const isLive = phase === 'executing' || phase === 'polling';
  const duration = formatDurationMs(stats.durationMs);
  const timeLabel = isLive
    ? formatElapsedColon(running.elapsedMs)
    : phase === 'cancelled'
      ? duration || formatElapsedColon(running.elapsedMs)
      : duration;
  const percent = isLive ? running.progressPercent : null;
  const rowsLabel =
    phase === 'completed' && stats.rowsReturned != null
      ? `${stats.rowsReturned.toLocaleString()} ${stats.rowsReturned === 1 ? 'row' : 'rows'}`
      : null;

  // Announced only on phase boundaries — never per-second/per-percent ticks.
  const phaseAnnouncement = isLive
    ? `${providerLabel} query running`
    : phase === 'completed'
      ? `${providerLabel} query completed`
      : phase === 'failed'
        ? `${providerLabel} query failed`
        : `${providerLabel} query cancelled`;

  /* ---------- Message reference (pin-to-context) ---------- */

  const argsSnapshot = useMemo(() => {
    const argsText =
      typeof toolCall.argsText === 'string'
        ? toolCall.argsText
        : JSON.stringify(toolCall.args || {}, null, 2);
    return normalizeSnapshot(argsText) || '(empty)';
  }, [toolCall.argsText, toolCall.args]);

  const resultSnapshot = useMemo(() => {
    if (!toolResultEnvelope) return null;
    const resultText =
      toolResultEnvelope.result_format === 'text'
        ? String(toolResultEnvelope.result ?? '')
        : safeStringify(toolResultEnvelope.result);
    const snapshot = normalizeSnapshot(
      [
        `tool: ${toolResultEnvelope.tool_name || toolCall.name}`,
        `ok: ${toolResultEnvelope.ok}`,
        `format: ${toolResultEnvelope.result_format}`,
        '',
        'result:',
        resultText,
      ].join('\n'),
    );
    return snapshot || null;
  }, [toolCall.name, toolResultEnvelope]);

  const messageReference = useMemo<MessageBlockReference>(() => {
    const callSourceMessageId =
      typeof parentMessageId === 'string' && parentMessageId.trim()
        ? parentMessageId.trim()
        : undefined;
    const callSourceSeqRaw = Number(parentMessageSeq);
    const callSourceMessageSeq =
      Number.isFinite(callSourceSeqRaw) && callSourceSeqRaw > 0
        ? Math.trunc(callSourceSeqRaw)
        : undefined;

    const resultSourceMessageId =
      typeof resultMessageId === 'string' && resultMessageId.trim()
        ? resultMessageId.trim()
        : undefined;
    const resultSourceSeqRaw = Number(resultMessageSeq);
    const resultSourceMessageSeq =
      Number.isFinite(resultSourceSeqRaw) && resultSourceSeqRaw > 0
        ? Math.trunc(resultSourceSeqRaw)
        : undefined;

    const messageId = resultSnapshot
      ? resultSourceMessageId || callSourceMessageId
      : callSourceMessageId;
    const messageSeq = resultSnapshot
      ? resultSourceMessageSeq !== undefined
        ? resultSourceMessageSeq
        : callSourceMessageSeq
      : callSourceMessageSeq;

    const normalizedResultIdentity =
      typeof resultMessageIdentityKey === 'string' && resultMessageIdentityKey.trim()
        ? resultMessageIdentityKey.trim()
        : undefined;
    const messageIdentity =
      normalizedResultIdentity ||
      (messageId
        ? `id:${messageId}`
        : messageSeq !== undefined
          ? `seq:${messageSeq}`
          : `tool_index:${toolCallIndex ?? 0}`);
    const callIdentity =
      String(toolCall.id || `index:${toolCallIndex ?? 0}`).trim() ||
      `index:${toolCallIndex ?? 0}`;
    const idPrefix = resultSnapshot ? 'tool_result' : 'tool_call';
    const id = `${idPrefix}:${messageIdentity}:${callIdentity}`;
    const kind: MessageBlockReference['kind'] = resultSnapshot ? 'tool_result' : 'tool_call';
    const title = resultSnapshot
      ? `Tool result - ${toolCall.name}`
      : `Tool call - ${toolCall.name}`;
    const contentSnapshot = resultSnapshot || argsSnapshot;
    return {
      id,
      key: id,
      kind,
      messageId,
      messageSeq,
      role: Role.Tool,
      title,
      label: title,
      preview: toPreview(contentSnapshot),
      contentSnapshot,
    };
  }, [
    argsSnapshot,
    parentMessageId,
    parentMessageSeq,
    resultMessageId,
    resultMessageIdentityKey,
    resultMessageSeq,
    resultSnapshot,
    toolCall.id,
    toolCall.name,
    toolCallIndex,
  ]);

  const pinnedSet = useMemo(
    () => new Set((pinnedMessageReferenceIds || []).map((id) => String(id || '').trim()).filter(Boolean)),
    [pinnedMessageReferenceIds],
  );
  const isMessageReferenced = pinnedSet.has(messageReference.id);

  /* ---------- Render ---------- */

  return (
    <div
      data-query-tool-call-id={toolCall.id || undefined}
      data-query-status={status}
      className={cn(
        'group relative min-w-0 max-w-full overflow-hidden rounded-md border transition-colors',
        'focus-within:ring-2 focus-within:ring-ring',
        phase === 'failed'
          ? 'border-destructive/40 bg-destructive/5 hover:bg-destructive/10'
          : 'border-border hover:bg-muted/40',
      )}
    >
      <div className="flex min-w-0 items-stretch">
      {/* Whole-card click target — opens the full audit overlay. The SQL
          preview lives behind the explicit chevron toggle on the right. */}
      <button
        type="button"
        onClick={(e) => {
          if (hasNonCollapsedSelectionWithin(e.currentTarget)) return;
          if (openTarget && onOpenArtifact) onOpenArtifact(openTarget);
          else setIsPreviewOpen((prev) => !prev);
        }}
        aria-label={canOpen ? 'Open full audit' : undefined}
        title={canOpen ? 'Open full audit' : undefined}
        aria-expanded={canOpen ? undefined : isPreviewOpen}
        aria-controls={!canOpen && isPreviewOpen ? previewId : undefined}
        className={cn(
          'flex min-w-0 flex-1 flex-col px-2.5 py-1.5 text-left',
          'focus-visible:outline-none',
          'cursor-pointer',
          canOpen && !isPreviewOpen && "after:absolute after:inset-0 after:content-['']",
        )}
      >
        <span className="flex w-full min-w-0 items-center gap-2">
          <span
            className="flex h-[21px] w-[21px] shrink-0 items-center justify-center overflow-hidden rounded-md border border-border bg-muted/50"
            title={providerLabel}
          >
            {connectorMeta ? (
              <ConnectionLogo
                meta={connectorMeta}
                className={cn(
                  'h-3.5 w-3.5 object-contain',
                  connectorMeta.mono && '[filter:brightness(0)_invert(1)]',
                )}
              />
            ) : (
              <Database size={12} className="text-muted-foreground" aria-hidden />
            )}
          </span>
          {/* Registry providers without a logo asset fall back to a text label
              so provider identity never disappears entirely. */}
          {!connectorMeta && (
            <span className="shrink-0 text-[12px] font-medium text-foreground">
              {providerLabel}
            </span>
          )}
          <StatusPill status={statusKindFor(status)} />
          <span
            className={cn(
              'min-w-0 flex-1 truncate text-[12px]',
              phase === 'failed' ? 'text-red-400' : 'text-muted-foreground',
            )}
            title={summaryText}
          >
            {summaryText}
          </span>
          <span className="flex shrink-0 items-center gap-x-2 font-mono text-[11px] tabular-nums text-muted-foreground">
            {timeLabel && <span>{timeLabel}</span>}
            {percent !== null && <span>{percent}%</span>}
            {rowsLabel && <span className="hidden sm:inline">{rowsLabel}</span>}
            {stats.jobId && (
              <span className="hidden md:inline">{`job ${shortId(stats.jobId)}`}</span>
            )}
          </span>
        </span>

        {isLive && (
          <span
            role="progressbar"
            aria-label="Query progress"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={percent === null ? undefined : percent}
            aria-valuetext={percent === null ? undefined : `${percent}%`}
            className={cn(
              'mt-1.5 block h-0.5 w-full overflow-hidden rounded-full',
              brand.progressTrack,
            )}
          >
            {percent === null ? (
              <span
                className={cn(
                  'block h-full w-1/3 rounded-full motion-safe:animate-[shimmer_1.5s_ease-in-out_infinite]',
                  brand.progressFill,
                )}
              />
            ) : (
              <span
                className={cn('block h-full rounded-full transition-all duration-700 ease-out', brand.progressFill)}
                style={{ width: `${percent}%` }}
              />
            )}
          </span>
        )}
        {phase === 'failed' && errorDetail && (
          <span className="mt-1 chat-text-wrap text-[11px] leading-snug text-red-400">
            {errorDetail}
          </span>
        )}
      </button>

      <span className="relative flex shrink-0 items-center gap-0.5 border-l border-border/60 px-1">
        <button
          type="button"
          onClick={() => setIsPreviewOpen((prev) => !prev)}
          aria-expanded={isPreviewOpen}
          aria-controls={isPreviewOpen ? previewId : undefined}
          aria-label="Toggle SQL preview"
          title="Toggle SQL preview"
          className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ChevronRight
            size={14}
            aria-hidden
            className={cn(
              'transition-transform duration-150',
              isPreviewOpen && 'rotate-90',
            )}
          />
        </button>
        {onToggleMessageReference && (
          <MessageReferenceButton
            isReferenced={isMessageReferenced}
            onToggle={() => onToggleMessageReference(messageReference)}
          />
        )}
      </span>
      </div>

      {isPreviewOpen && (
        <div
          id={previewId}
          className="motion-safe:animate-in motion-safe:fade-in motion-safe:slide-in-from-top-1 motion-safe:duration-150 border-t border-border/60 bg-muted/20 px-3 py-2"
        >
          {sql ? (
            <pre className="chat-text-wrap max-h-48 overflow-y-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-snug text-muted-foreground">
              {sql}
            </pre>
          ) : (
            <p className="text-[11px] text-muted-foreground/60">
              No SQL captured for this query.
            </p>
          )}
        </div>
      )}

      <span role="status" aria-live="polite" className="sr-only">
        {phaseAnnouncement}
      </span>
    </div>
  );
};

export default QueryExecutionCard;
