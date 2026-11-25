import React from 'react';
import { ExternalLink } from 'lucide-react';
import { CopyIcon, Stat, formatElapsedColon, shortId } from './atoms';
import type { QueryInspectorState } from './useQueryInspectorState';

/**
 * CancelledResponse — quiet companion to RunningResponse. Same response-zone
 * structure (status line + telemetry strip) minus the progress bar and the
 * dashed empty-state, replaced with a neutral "Results unavailable" line.
 * Still surfaces the native job link so users can verify the cancellation
 * in the source warehouse.
 */
export function CancelledResponse({ state }: { state: QueryInspectorState }) {
  const { running, stats, consoleUrl, providerLabel } = state;
  const elapsed = formatElapsedColon(running.elapsedMs);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="inline-flex items-center gap-2 text-[13px] font-medium text-muted-foreground">
          <span>Cancelled by user</span>
        </div>
        <span
          aria-live="polite"
          role="status"
          className="inline-flex items-center gap-2 text-[11px] text-muted-foreground"
        >
          <span className="font-mono tabular-nums">{elapsed} elapsed</span>
        </span>
        {consoleUrl && (
          <a
            href={consoleUrl}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={`Open in ${providerLabel}`}
            className="ml-auto inline-flex items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-[11px] text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
          >
            Open in {providerLabel}
            <ExternalLink size={11} aria-hidden />
          </a>
        )}
      </div>

      <div>
        <div className="mb-2 text-xs text-muted-foreground/60">Results</div>
        <div className="rounded-md border border-border bg-muted/30 px-4 py-4 text-center">
          <p className="text-[13px] leading-[1.55] text-muted-foreground">
            Results unavailable — query was cancelled.
          </p>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {stats.jobId && (
          <span className="inline-flex items-center gap-1 font-mono text-[11px] text-muted-foreground">
            {`job ${shortId(stats.jobId)}`}
            <CopyIcon value={stats.jobId} label="Copy job id" />
          </span>
        )}
        {stats.jobLocation && <Stat label={`region ${stats.jobLocation}`} mono />}
        {(stats.warehouseName || stats.warehouseId) && (
          <Stat
            label={`warehouse ${stats.warehouseName || shortId(stats.warehouseId)}`}
            mono
          />
        )}
      </div>
    </div>
  );
}
