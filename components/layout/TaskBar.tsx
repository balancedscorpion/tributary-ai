import React, { useState, useEffect, useMemo } from 'react';
import {
  Clock,
  ChevronDown,
  ChevronUp,
  Square,
  ExternalLink,
  X,
  Loader2,
  CheckCircle2,
  AlertTriangle,
  XCircle,
  HelpCircle,
} from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import CodeBlock from '@/components/chat/CodeBlock';
import { useQueryMonitor } from '@/hooks/useQueryMonitor';
import { getSharedQueryStatusKey, useSharedQueryStatuses } from '@/hooks/useSharedQueryStatus';
import { collectAsyncQueryPollTargets } from '@/lib/asyncQueryPollTargets';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';
import {
  formatElapsed,
  formatDuration,
  formatRowCount,
  formatBytes,
} from '@/lib/formatters';
import {
  getQueryProviderBrand,
  getQueryProviderLabel,
  getQueryProviderProgressLinkLabel,
  resolveQueryNativeJobLink,
} from '@/lib/queryProviderRegistry';
import {
  deriveQueryProgressDisplay,
} from '@/lib/queryProgress';
import type { Artifact, TrackedQuery, QueryJobStatus } from '@/types';

interface QueryStatusVisual {
  icon: React.ComponentType<{ size?: number; className?: string }>;
  label: string;
  textColor: string;
  dotColor: string;
  progressTrackColor: string;
  progressFillColor: string;
  progressTextColor: string;
  animateIcon?: boolean;
}

const STATUS_VISUAL: Record<QueryJobStatus, QueryStatusVisual> = {
  reserved: {
    icon: Clock,
    label: 'Query queued...',
    textColor: 'text-blue-400',
    dotColor: 'bg-blue-400',
    progressTrackColor: 'bg-blue-500/20',
    progressFillColor: 'bg-blue-400',
    progressTextColor: 'text-blue-400/70',
  },
  profiling: {
    icon: Loader2,
    label: 'Profiling query...',
    textColor: 'text-blue-400',
    dotColor: 'bg-blue-400',
    progressTrackColor: 'bg-blue-500/20',
    progressFillColor: 'bg-blue-400',
    progressTextColor: 'text-blue-400/70',
    animateIcon: true,
  },
  submission_unknown: {
    icon: AlertTriangle,
    label: 'Reconciling submission...',
    textColor: 'text-amber-400',
    dotColor: 'bg-amber-400',
    progressTrackColor: 'bg-amber-500/20',
    progressFillColor: 'bg-amber-400',
    progressTextColor: 'text-amber-400/70',
  },
  submitted: {
    icon: Loader2,
    label: 'Query submitted...',
    textColor: 'text-blue-400',
    dotColor: 'bg-blue-400',
    progressTrackColor: 'bg-blue-500/20',
    progressFillColor: 'bg-blue-400',
    progressTextColor: 'text-blue-400/70',
    animateIcon: true,
  },
  running: {
    icon: Loader2,
    label: 'Query running...',
    textColor: 'text-blue-400',
    dotColor: 'bg-blue-400',
    progressTrackColor: 'bg-blue-500/20',
    progressFillColor: 'bg-blue-400',
    progressTextColor: 'text-blue-400/70',
    animateIcon: true,
  },
  cancel_requested: {
    icon: Loader2,
    label: 'Cancellation requested...',
    textColor: 'text-amber-400',
    dotColor: 'bg-amber-400',
    progressTrackColor: 'bg-amber-500/20',
    progressFillColor: 'bg-amber-400',
    progressTextColor: 'text-amber-400/70',
    animateIcon: true,
  },
  cancelling: {
    icon: Loader2,
    label: 'Confirming cancellation...',
    textColor: 'text-amber-400',
    dotColor: 'bg-amber-400',
    progressTrackColor: 'bg-amber-500/20',
    progressFillColor: 'bg-amber-400',
    progressTextColor: 'text-amber-400/70',
    animateIcon: true,
  },
  completed: {
    icon: CheckCircle2,
    label: 'Query completed',
    textColor: 'text-green-400',
    dotColor: 'bg-green-400',
    progressTrackColor: 'bg-green-500/20',
    progressFillColor: 'bg-green-400',
    progressTextColor: 'text-green-400/80',
  },
  failed: {
    icon: AlertTriangle,
    label: 'Query failed',
    textColor: 'text-red-400',
    dotColor: 'bg-red-400',
    progressTrackColor: 'bg-red-500/20',
    progressFillColor: 'bg-red-400',
    progressTextColor: 'text-red-400/80',
  },
  cancelled: {
    icon: XCircle,
    label: 'Query cancelled',
    textColor: 'text-muted-foreground',
    dotColor: 'bg-muted-foreground',
    progressTrackColor: 'bg-muted/40',
    progressFillColor: 'bg-muted-foreground/70',
    progressTextColor: 'text-muted-foreground',
  },
  expired: {
    icon: AlertTriangle,
    label: 'Query budget expired',
    textColor: 'text-red-400',
    dotColor: 'bg-red-400',
    progressTrackColor: 'bg-red-500/20',
    progressFillColor: 'bg-red-400',
    progressTextColor: 'text-red-400/80',
  },
  orphaned: {
    icon: AlertTriangle,
    label: 'Query recovery required',
    textColor: 'text-red-400',
    dotColor: 'bg-red-400',
    progressTrackColor: 'bg-red-500/20',
    progressFillColor: 'bg-red-400',
    progressTextColor: 'text-red-400/80',
  },
  unknown: {
    icon: HelpCircle,
    label: 'Query status unknown',
    textColor: 'text-muted-foreground',
    dotColor: 'bg-muted-foreground',
    progressTrackColor: 'bg-muted/40',
    progressFillColor: 'bg-muted-foreground/70',
    progressTextColor: 'text-muted-foreground',
  },
};

function toProgressNumber(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return value >= 0 ? value : null;
  if (typeof value === 'string') {
    const n = Number(value.trim());
    return Number.isFinite(n) && n >= 0 ? n : null;
  }
  return null;
}

interface TaskBarProps {
  activeThreadId: string | null;
  artifactsById: Record<string, Artifact>;
  onNavigateToQuery: (query: TrackedQuery) => void;
  onCancelQuery: (jobId: string, ownerToken?: string, threadId?: string) => void;
  onOpenArtifact: (artifactId: string) => void;
}

function queryName(query: TrackedQuery): string {
  if (query.sqlPreview) {
    const trimmed = query.sqlPreview.trim();
    const match = trimmed.match(/^SELECT\s+.*?\s+FROM\s+[`"]?(\S+?)[`"]?(?:\s|$)/i);
    if (match) return match[1];
  }
  return 'Query';
}

function queryLabel(query: TrackedQuery): string {
  const description = String(query.description ?? query.queryMetadata?.description ?? '').trim();
  return description || queryName(query);
}

function querySqlText(query: TrackedQuery | null | undefined): string | undefined {
  if (!query) return undefined;
  const candidates = [
    query.sql,
    query.queryMetadata?.sql,
    query.sqlPreview,
    query.queryMetadata?.sql_preview,
  ];
  for (const value of candidates) {
    if (typeof value !== 'string') continue;
    const trimmed = value.trim();
    if (trimmed) return trimmed;
  }
  return undefined;
}

function resolveNativeLink(query: TrackedQuery) {
  const qm = query.queryMetadata;
  return resolveQueryNativeJobLink(query.provider, {
    providerJobId: qm?.provider_job_id ?? undefined,
    providerJobUrl: qm?.provider_job_url ?? undefined,
    warehouseJobId: qm?.warehouse_job_id ?? undefined,
    statementId: qm?.statement_id ?? undefined,
    queryId: qm?.query_id ?? undefined,
    projectId: qm?.project_id ?? undefined,
    accountIdentifier: qm?.account_identifier ?? undefined,
    location: qm?.job_location ?? qm?.location ?? undefined,
    host: qm?.host ?? undefined,
    url: qm?.url ?? undefined,
    jobId: normalizeManagedQueryJobId(query.jobId),
  });
}

function resolveRowsReturned(query: TrackedQuery): number | undefined {
  const direct = toProgressNumber(query.rowsReturned);
  if (direct != null) return Math.trunc(direct);
  const qm = query.queryMetadata;
  const fromMetadata = toProgressNumber(qm?.rows_returned) ?? toProgressNumber(qm?.row_count);
  return fromMetadata != null ? Math.trunc(fromMetadata) : undefined;
}

interface DerivedQueryUiState {
  status: QueryJobStatus;
  isRunning: boolean;
  isComplete: boolean;
  statusVisual: QueryStatusVisual;
  elapsedMs: number;
  frozenDurationMs: number;
  timerText: string;
  progressPercent: number | null;
  showProgress: boolean;
}

interface QueryLiveStatusSnapshot {
  status?: QueryJobStatus;
  progress_percent?: unknown;
  progress_pct?: unknown;
  elapsed_ms?: unknown;
}

function deriveQueryUiState(
  query: TrackedQuery,
  nowMs: number,
  liveStatus?: QueryLiveStatusSnapshot,
): DerivedQueryUiState {
  const progress = deriveQueryProgressDisplay({
    status: query.status,
    startedAtMs: query.startedAtMs,
    completedAtMs: query.completedAtMs,
    durationMs: query.durationMs,
    nowMs,
    liveStatus,
    metadataProgressPercent: query.queryMetadata?.progress_percent,
    metadataProgressPct: query.queryMetadata?.progress_pct,
  });
  const status = progress.status;
  const statusVisual = STATUS_VISUAL[status] || STATUS_VISUAL.running;

  return {
    status,
    isRunning: progress.isRunning,
    isComplete: progress.isComplete,
    statusVisual,
    elapsedMs: progress.elapsedMs,
    frozenDurationMs: progress.frozenDurationMs,
    timerText: progress.isRunning ? formatElapsed(progress.elapsedMs) : formatDuration(progress.frozenDurationMs),
    progressPercent: progress.progressPercent,
    showProgress: progress.showProgress,
  };
}

function TaskBarRow({
  query,
  ui,
  onSelect,
  onCancel,
  onDismiss,
}: {
  query: TrackedQuery;
  ui: DerivedQueryUiState;
  onSelect: () => void;
  onCancel?: () => void;
  onDismiss: () => void;
}) {
  const isRunning = ui.isRunning;
  const isComplete = ui.isComplete;
  const providerLabel = getQueryProviderLabel(query.provider);
  const brand = getQueryProviderBrand(query.provider);
  const BrandIcon = brand.icon;
  const bytesProcessed = query.queryMetadata?.total_bytes_processed;
  const nativeLink = resolveNativeLink(query);
  const linkLabel = getQueryProviderProgressLinkLabel(query.provider);
  const rowsReturned = resolveRowsReturned(query);
  const label = queryLabel(query);
  const sqlPreviewText = typeof query.sqlPreview === 'string' ? query.sqlPreview.trim() : '';
  const showSqlPreview = Boolean(sqlPreviewText) && sqlPreviewText !== label;

  return (
    <div
      id={`task-${query.artifactId}`}
      data-job-id={query.jobId}
      role="button"
      tabIndex={0}
      onClick={onSelect}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onSelect();
        }
      }}
      className={`border-l-[3px] ${brand.accentBorder} px-3 py-2.5 hover:bg-muted/50 transition-colors cursor-pointer group border-b border-border/30`}
    >
      {/* Line 1: Provider pill, label, timer, actions */}
      <div className="flex items-center gap-2">
        <span className={`inline-flex items-center gap-1 text-[10px] font-semibold px-1.5 py-0.5 rounded-md shrink-0 ${brand.badgeBg} ${brand.badgeText}`}>
          <BrandIcon size={10} />
          {providerLabel}
        </span>
        <span
          className="text-xs font-medium text-foreground truncate flex-1 min-w-0"
          title={label}
        >
          {label}
        </span>
        <span className="text-[11px] font-mono text-muted-foreground shrink-0">
          {ui.timerText}
        </span>

        {/* Cancel button on hover for running */}
        {isRunning && onCancel && (
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  onCancel();
                }}
                className="shrink-0 p-1 rounded text-muted-foreground/40 hover:text-destructive hover:bg-destructive/10 transition-all opacity-0 group-hover:opacity-100"
              >
                <Square size={10} fill="currentColor" />
              </button>
            </TooltipTrigger>
            <TooltipContent>Cancel query</TooltipContent>
          </Tooltip>
        )}

        {/* Dismiss button on hover */}
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                onDismiss();
              }}
              className="shrink-0 p-0.5 rounded text-muted-foreground/30 hover:text-muted-foreground hover:bg-muted/60 transition-all opacity-0 group-hover:opacity-100"
            >
              <X size={10} />
            </button>
          </TooltipTrigger>
          <TooltipContent>Dismiss</TooltipContent>
        </Tooltip>
      </div>

      {/* Line 2: SQL preview, metadata, progress */}
      <div className="flex items-center gap-2 mt-1">
        {showSqlPreview ? (
          <span
            className="text-[10px] font-mono text-muted-foreground/50 truncate flex-1 min-w-0"
            title={sqlPreviewText}
          >
            {sqlPreviewText}
          </span>
        ) : (
          <div className="flex-1" />
        )}

        {/* Job ID hyperlink to platform */}
        {nativeLink && (
          <Tooltip>
            <TooltipTrigger asChild>
              <a
                href={nativeLink.url}
                target="_blank"
                rel="noopener noreferrer"
                onClick={(e) => e.stopPropagation()}
                className="text-[10px] font-mono text-blue-400/70 hover:text-blue-400 hover:underline shrink-0 flex items-center gap-0.5 max-w-[80px] truncate"
              >
                {nativeLink.id}
                <ExternalLink size={8} className="shrink-0" />
              </a>
            </TooltipTrigger>
            <TooltipContent>{linkLabel}</TooltipContent>
          </Tooltip>
        )}

        {/* Bytes scanned */}
        {bytesProcessed != null && (
          <span className="text-[10px] text-muted-foreground/60 shrink-0">
            {formatBytes(bytesProcessed)}
          </span>
        )}

        {/* Row count for completed */}
        {isComplete && rowsReturned != null && (
          <span className="text-[10px] text-muted-foreground/60 shrink-0">
            {formatRowCount(rowsReturned)} rows
          </span>
        )}

        {/* Wider progress bar with brand colors */}
        {ui.showProgress && ui.progressPercent != null && (
          <div className="shrink-0 flex items-center gap-1">
            <div className={`w-24 h-1.5 rounded-full overflow-hidden ${brand.progressTrack}`}>
              <div
                className={`h-full rounded-full transition-all duration-700 ease-out ${brand.progressFill}`}
                style={{ width: `${ui.progressPercent}%` }}
              />
            </div>
            <span className={`text-[9px] font-mono w-7 text-right ${brand.badgeText}`}>
              {ui.progressPercent}%
            </span>
          </div>
        )}
      </div>
    </div>
  );
}

function TaskBarExpandedDetail({
  query,
  ui,
  onNavigate,
  onOpenArtifact,
  onCancel,
  onCollapse,
}: {
  query: TrackedQuery;
  ui: DerivedQueryUiState;
  onNavigate: () => void;
  onOpenArtifact: () => void;
  onCancel?: () => void;
  onCollapse: () => void;
}) {
  const brand = getQueryProviderBrand(query.provider);
  const nativeLink = resolveNativeLink(query);
  const linkLabel = getQueryProviderProgressLinkLabel(query.provider);
  const bytesProcessed = query.queryMetadata?.total_bytes_processed;
  const rowsReturned = resolveRowsReturned(query);
  const sqlText = querySqlText(query) || '-- SQL text unavailable for this query.';

  return (
    <div className={`border-l-[3px] ${brand.accentBorder} px-3 py-3 bg-muted/20 border-b border-border/30`}>
      {/* SQL */}
      <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground mb-1">SQL</div>
      <div className="max-h-[200px] overflow-y-auto rounded-md">
        <CodeBlock language="sql">{sqlText}</CodeBlock>
      </div>

      {/* Metadata row */}
      <div className="flex items-center gap-3 mt-3 flex-wrap text-[11px] text-muted-foreground">
        {nativeLink && (
          <a
            href={nativeLink.url}
            target="_blank"
            rel="noopener noreferrer"
            className="font-mono text-blue-400/70 hover:text-blue-400 hover:underline inline-flex items-center gap-1"
            title={linkLabel}
          >
            <span className="truncate max-w-[200px]">{nativeLink.id}</span>
            <ExternalLink size={11} />
          </a>
        )}
        {bytesProcessed != null && (
          <span>{formatBytes(bytesProcessed)} scanned</span>
        )}
        {rowsReturned != null && (
          <span>{formatRowCount(rowsReturned)} rows</span>
        )}
      </div>

      {/* Actions */}
      <div className="flex items-center gap-2 mt-3">
        <Button type="button" size="sm" variant="outline" onClick={() => { onNavigate(); onCollapse(); }}>
          Show in thread
        </Button>
        {!ui.isRunning && (
          <Button type="button" size="sm" variant="secondary" onClick={() => { onOpenArtifact(); onCollapse(); }}>
            Open result
          </Button>
        )}
        {ui.isRunning && onCancel && (
          <Button type="button" size="sm" variant="destructive" onClick={onCancel}>
            Cancel
          </Button>
        )}
      </div>
    </div>
  );
}

const TaskBar: React.FC<TaskBarProps> = ({
  activeThreadId,
  artifactsById,
  onNavigateToQuery,
  onCancelQuery,
  onOpenArtifact,
}) => {
  // Subscribe to live job status straight from the artifacts map (rather
  // than from the monitor's output) so terminal polling results can feed the
  // monitor itself — otherwise the header count only updates when the
  // placeholder artifact hydrates, which can lag completion.
  const pollTargets = useMemo(
    () => collectAsyncQueryPollTargets(artifactsById, activeThreadId),
    [artifactsById, activeThreadId],
  );
  const liveStatusByKey = useSharedQueryStatuses(pollTargets);
  const { runningQueries, recentQueries, totalRunning } = useQueryMonitor(
    artifactsById,
    activeThreadId,
    liveStatusByKey,
  );
  const [dismissedIds, setDismissedIds] = useState<Set<string>>(() => new Set());
  const [collapsed, setCollapsed] = useState(false);
  const [expandedQueryArtifactId, setExpandedQueryArtifactId] = useState<string | null>(null);
  const [nowMs, setNowMs] = useState(() => Date.now());

  const allQueries = useMemo(() => [...runningQueries, ...recentQueries], [runningQueries, recentQueries]);
  const queryStatusKeyByArtifactId = useMemo(() => {
    const keyByID = new Map<string, string>();
    for (const query of allQueries) {
      const key = getSharedQueryStatusKey(
        query.jobId,
        query.threadId || activeThreadId || '',
        query.runId,
        query.queryObligationId,
      );
      if (!key) continue;
      keyByID.set(query.artifactId, key);
    }
    return keyByID;
  }, [activeThreadId, allQueries]);

  const visibleQueries = allQueries.filter((q) => !dismissedIds.has(q.artifactId));
  const hasTasks = visibleQueries.length > 0;
  const queryUiByArtifactId = useMemo(() => {
    const byId = new Map<string, DerivedQueryUiState>();
    for (const query of allQueries) {
      const baseUI = deriveQueryUiState(query, nowMs);
      const statusKey = queryStatusKeyByArtifactId.get(query.artifactId);
      const liveStatus = statusKey ? liveStatusByKey[statusKey] : undefined;
      byId.set(query.artifactId, liveStatus ? deriveQueryUiState(query, nowMs, liveStatus) : baseUI);
    }
    return byId;
  }, [allQueries, nowMs, queryStatusKeyByArtifactId, liveStatusByKey]);
  useEffect(() => {
    setNowMs(Date.now());
    if (totalRunning <= 0) return;
    const id = setInterval(() => {
      setNowMs(Date.now());
    }, 1000);
    return () => clearInterval(id);
  }, [totalRunning]);

  useEffect(() => {
    if (expandedQueryArtifactId && !allQueries.some((q) => q.artifactId === expandedQueryArtifactId)) {
      setExpandedQueryArtifactId(null);
    }
  }, [expandedQueryArtifactId, allQueries]);

  const handleRowClick = (query: TrackedQuery) => {
    setExpandedQueryArtifactId((prev) => prev === query.artifactId ? null : query.artifactId);
  };

  const handleDismiss = (artifactId: string) => {
    if (expandedQueryArtifactId === artifactId) {
      setExpandedQueryArtifactId(null);
    }
    setDismissedIds((prev) => {
      const next = new Set(prev);
      next.add(artifactId);
      return next;
    });
  };

  const handleClearAll = () => {
    if (expandedQueryArtifactId && visibleQueries.some((q) => q.artifactId === expandedQueryArtifactId)) {
      setExpandedQueryArtifactId(null);
    }
    setDismissedIds((prev) => {
      const next = new Set(prev);
      for (const q of visibleQueries) next.add(q.artifactId);
      return next;
    });
  };

  return (
    <div
      className={`border-b border-border bg-card/80 backdrop-blur-md transition-all duration-300 ease-in-out ${
        hasTasks ? 'max-h-[300px] opacity-100' : 'max-h-0 opacity-0 overflow-hidden'
      }`}
    >
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5 hover:bg-muted/30 transition-colors">
        <button
          type="button"
          onClick={() => setCollapsed((prev) => !prev)}
          className="flex-1 flex items-center gap-2 text-left"
        >
          <Clock size={12} className="text-muted-foreground" />
          <span className="text-xs font-medium text-muted-foreground">Active tasks</span>
          {totalRunning > 0 ? (
            <Badge
              variant="secondary"
              className="text-[10px] px-1.5 py-0 bg-blue-500/10 text-blue-400 border-blue-500/20"
            >
              {totalRunning} running
            </Badge>
          ) : hasTasks ? (
            <Badge
              variant="secondary"
              className="text-[10px] px-1.5 py-0 bg-green-500/10 text-green-400 border-green-500/20"
            >
              All complete
            </Badge>
          ) : null}
          {/* Provider-colored dots when collapsed */}
          {collapsed && visibleQueries.length > 0 && (
            <div className="flex items-center gap-1 ml-1">
              {visibleQueries.filter((q) => {
                const qUi = queryUiByArtifactId.get(q.artifactId);
                return qUi?.isRunning;
              }).slice(0, 8).map((q) => (
                <span
                  key={q.artifactId}
                  className={`w-1.5 h-1.5 rounded-full ${getQueryProviderBrand(q.provider).dotColor} animate-pulse`}
                />
              ))}
            </div>
          )}
          <div className="flex-1" />
          {collapsed ? (
            <ChevronDown size={12} className="text-muted-foreground" />
          ) : (
            <ChevronUp size={12} className="text-muted-foreground" />
          )}
        </button>

        {/* Clear all button */}
        {visibleQueries.length > 1 && (
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                onClick={handleClearAll}
                className="shrink-0 p-0.5 rounded text-muted-foreground/40 hover:text-muted-foreground hover:bg-muted/60 transition-colors"
              >
                <X size={12} />
              </button>
            </TooltipTrigger>
            <TooltipContent>Clear all</TooltipContent>
          </Tooltip>
        )}
      </div>

      {/* Task rows */}
      {/* Task rows with expand-in-place */}
      {!collapsed && (
        <div className="overflow-y-auto max-h-[300px]">
          {visibleQueries.map((q) => {
            const ui = queryUiByArtifactId.get(q.artifactId) ?? deriveQueryUiState(q, nowMs);
            const isExpanded = expandedQueryArtifactId === q.artifactId;
            const managedJobId = normalizeManagedQueryJobId(q.jobId);
            return (
              <React.Fragment key={q.artifactId}>
                <TaskBarRow
                  query={q}
                  ui={ui}
                  onSelect={() => handleRowClick(q)}
                  onCancel={
                    managedJobId && (ui.status === 'running' || ui.status === 'submitted')
                      ? () => onCancelQuery(
                        managedJobId,
                        q.ownerToken,
                        q.threadId || activeThreadId || undefined,
                      )
                      : undefined
                  }
                  onDismiss={() => handleDismiss(q.artifactId)}
                />
                {isExpanded && (
                  <TaskBarExpandedDetail
                    query={q}
                    ui={ui}
                    onNavigate={() => onNavigateToQuery(q)}
                    onOpenArtifact={() => onOpenArtifact(q.artifactId)}
                    onCancel={
                      managedJobId && (ui.status === 'running' || ui.status === 'submitted')
                        ? () => onCancelQuery(
                          managedJobId,
                          q.ownerToken,
                          q.threadId || activeThreadId || undefined,
                        )
                        : undefined
                    }
                    onCollapse={() => setExpandedQueryArtifactId(null)}
                  />
                )}
              </React.Fragment>
            );
          })}
        </div>
      )}
    </div>
  );
};

export default React.memo(TaskBar);
