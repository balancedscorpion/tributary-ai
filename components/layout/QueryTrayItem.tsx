import React, { useState, useEffect } from 'react';
import {
  Loader2, CheckCircle2, AlertTriangle, XCircle, Square,
} from 'lucide-react';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import {
  formatElapsed,
  formatDuration,
  formatRelativeTime,
  formatRowCount,
} from '@/lib/formatters';
import { deriveQueryProgressDisplay } from '@/lib/queryProgress';
import type { TrackedQuery, QueryJobStatus } from '@/types';
import { getQueryProviderLabel, getQueryProviderBrand } from '@/lib/queryProviderRegistry';

const STATUS_CONFIG: Record<QueryJobStatus, {
  icon: React.ElementType;
  label: string;
  color: string;
  animate: boolean;
}> = {
  reserved:  { icon: Loader2, label: 'Queued', color: 'text-amber-500', animate: true },
  profiling: { icon: Loader2, label: 'Profiling', color: 'text-blue-400', animate: true },
  submission_unknown: { icon: AlertTriangle, label: 'Reconciling', color: 'text-amber-500', animate: false },
  submitted: { icon: Loader2, label: 'Submitted', color: 'text-amber-500', animate: true },
  running:   { icon: Loader2, label: 'Running',   color: 'text-amber-500', animate: true },
  cancel_requested: { icon: Loader2, label: 'Cancel requested', color: 'text-amber-500', animate: true },
  cancelling: { icon: Loader2, label: 'Cancelling', color: 'text-amber-500', animate: true },
  completed: { icon: CheckCircle2, label: 'Completed', color: 'text-green-400', animate: false },
  failed:    { icon: AlertTriangle, label: 'Failed', color: 'text-destructive', animate: false },
  cancelled: { icon: XCircle, label: 'Cancelled', color: 'text-muted-foreground', animate: false },
  expired:   { icon: AlertTriangle, label: 'Budget expired', color: 'text-destructive', animate: false },
  orphaned:  { icon: AlertTriangle, label: 'Recovery needed', color: 'text-destructive', animate: false },
  unknown:   { icon: Loader2, label: 'Unknown', color: 'text-muted-foreground', animate: false },
};

interface QueryTrayItemProps {
  query: TrackedQuery;
  liveStatus?: {
    status?: QueryJobStatus;
    progress_percent?: unknown;
    progress_pct?: unknown;
    elapsed_ms?: unknown;
  };
  onNavigate: () => void;
  onCancel?: () => void;
}

const QueryTrayItem: React.FC<QueryTrayItemProps> = ({
  query, liveStatus, onNavigate, onCancel,
}) => {
  // Live elapsed timer for running queries.
  const [elapsed, setElapsed] = useState(() =>
    Date.now() - query.startedAtMs
  );
  const ui = deriveQueryProgressDisplay({
    status: query.status,
    startedAtMs: query.startedAtMs,
    completedAtMs: query.completedAtMs,
    durationMs: query.durationMs,
    nowMs: query.startedAtMs + elapsed,
    liveStatus,
    metadataProgressPercent: query.queryMetadata?.progress_percent,
    metadataProgressPct: query.queryMetadata?.progress_pct,
  });
  const config = STATUS_CONFIG[ui.status] ?? STATUS_CONFIG.unknown;
  const Icon = config.icon;
  const isRunning = ui.isRunning;
  const providerLabel = getQueryProviderLabel(query.provider);
  const brand = getQueryProviderBrand(query.provider);
  const BrandIcon = brand.icon;
  const completedLabel = !isRunning ? formatRelativeTime(query.completedAtMs) : '';

  useEffect(() => {
    if (!isRunning) return;
    setElapsed((prev) => Math.max(prev, Date.now() - query.startedAtMs));
    const id = setInterval(() => {
      setElapsed((prev) => Math.max(prev, Date.now() - query.startedAtMs));
    }, 1000);
    return () => clearInterval(id);
  }, [isRunning, query.startedAtMs]);

  const progressPercent = ui.progressPercent;

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onNavigate}
      onKeyDown={(event) => {
        if (event.target !== event.currentTarget) return;
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          onNavigate();
        }
      }}
      className={`border-l-[3px] ${brand.accentBorder} rounded-r-lg border border-l-0 border-border/40 bg-card/30 hover:bg-card/60 transition-colors cursor-pointer group focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 p-3`}
    >
      {/* Line 1: Provider + Status + Timer */}
      <div className="flex items-center gap-2">
        <span className={`inline-flex items-center gap-1 text-[10px] font-semibold px-1.5 py-0.5 rounded-md ${brand.badgeBg} ${brand.badgeText}`}>
          <BrandIcon size={10} />
          {providerLabel}
        </span>
        <Icon size={12} className={`${config.color} ${config.animate ? 'animate-spin' : ''}`} />
        <span className={`text-[10px] font-medium ${config.color}`}>
          {config.label}
        </span>
        <div className="flex-1" />
        <span className="text-[10px] font-mono text-muted-foreground">
          {isRunning
            ? formatElapsed(ui.elapsedMs)
            : formatDuration(ui.frozenDurationMs)}
        </span>
        {!isRunning && completedLabel && (
          <span className="text-[10px] text-muted-foreground/70">
            {completedLabel}
          </span>
        )}
      </div>

      {/* Line 2: SQL preview */}
      {query.sqlPreview && (
        <div className="mt-1.5">
          <span className="text-[11px] text-muted-foreground/60 font-mono truncate block">
            {query.sqlPreview}
          </span>
        </div>
      )}

      {/* Line 3: Progress (running) or Stats (completed) */}
      <div className="flex items-center gap-2 mt-2">
        {isRunning && ui.showProgress && progressPercent != null ? (
          <>
            <div className={`h-1.5 flex-1 rounded-full overflow-hidden ${brand.progressTrack}`}>
              <div
                className={`h-full rounded-full transition-all duration-700 ease-out ${brand.progressFill}`}
                style={{ width: `${Math.max(progressPercent, 5)}%` }}
              />
            </div>
            <span className={`text-[10px] font-mono tabular-nums ${brand.badgeText}`}>
              {progressPercent}%
            </span>
          </>
        ) : !isRunning ? (
          <div className="flex items-center gap-2 text-[10px] text-muted-foreground/60">
            {query.rowsReturned != null && (
              <span>{formatRowCount(query.rowsReturned)} rows</span>
            )}
          </div>
        ) : null}

        {/* Cancel button (running queries only, visible on hover) */}
        {isRunning && onCancel && (
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                onClick={(e) => { e.stopPropagation(); onCancel(); }}
                className="shrink-0 p-1 rounded text-muted-foreground/40 hover:text-destructive hover:bg-destructive/10 transition-all opacity-0 group-hover:opacity-100"
              >
                <Square size={10} fill="currentColor" />
              </button>
            </TooltipTrigger>
            <TooltipContent>Cancel query</TooltipContent>
          </Tooltip>
        )}
      </div>
    </div>
  );
};

export default QueryTrayItem;
