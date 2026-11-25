import React from 'react';
import { ExternalLink, Loader2, Square } from 'lucide-react';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';
import { CopyIcon, Stat, formatElapsedColon, shortId } from './atoms';
import type { QueryInspectorState } from './useQueryInspectorState';

/**
 * RunningResponse — the response zone shown while a query is submitted, in
 * flight, or polling. Replaces the old raw-JSON dump with: status line +
 * progress bar + cancel/open-in actions + an explicit empty-state card that
 * tells the user where results will appear.
 *
 * Strict requirements:
 *  - No raw JSON. Ever.
 *  - Cancel button only shows when cancel is actually possible (provider URL
 *    available + non-terminal phase).
 *  - Provider colour comes through brand.textActive / brand.progressFill so
 *    all four providers feel correctly branded.
 *  - Live region announces percent updates via aria-live="polite".
 */
export function RunningResponse({ state }: { state: QueryInspectorState }) {
  const { brand, providerLabel, running, stats, consoleUrl, phase, status, errorContext } = state;
  const elapsed = formatElapsedColon(running.elapsedMs);
  const percent = running.progressPercent;
  const showShimmer = percent === null;
  const statusText = (() => {
    if (status === 'reserved') return 'Waiting for query capacity';
    if (status === 'profiling') return 'Profiling query before submission';
    if (status === 'submission_unknown') return 'Reconciling query submission';
    if (status === 'cancel_requested' || status === 'cancelling') return 'Confirming cancellation';
    if (phase === 'executing') return 'Executing query';
    return `Running on ${providerLabel}`;
  })();
  const resultMessage =
    status === 'profiling'
      ? 'The query is durably reserved while its cost and execution plan are checked. It has not been submitted remotely and will continue from this same job identity.'
      : status === 'submission_unknown'
      ? 'The submission outcome is being reconciled. The SQL will not be submitted again blindly.'
      : status === 'cancel_requested' || status === 'cancelling'
        ? 'Cancellation was requested. This stays active until the provider confirms a terminal state.'
        : 'Query is running. Results will appear here when the job completes.';
  const healthWarning =
    stats.monitorHealth === 'lost'
      ? 'Provider monitoring is temporarily unavailable. The last known execution state is preserved and polling will continue.'
      : stats.monitorHealth === 'degraded'
        ? 'Some provider status checks failed. The query itself has not been marked failed.'
        : stats.policyHealth === 'suspected_stalled'
          ? 'Provider telemetry has not advanced across several observation windows. The query is still running while alternatives are evaluated.'
          : stats.policyHealth === 'soft_queue_exceeded'
            ? 'The query has been queued longer than its soft budget. It remains active while capacity and provider state are evaluated.'
            : stats.policyHealth === 'soft_execution_exceeded'
              ? 'The query has exceeded its soft execution budget. It remains active; elapsed time alone is not treated as failure.'
              : stats.cancelOutcome === 'unconfirmed'
                ? 'The provider has not confirmed cancellation. The query remains active and observable until a terminal state is known.'
          : '';

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div
          className={cn('inline-flex items-center gap-2 text-[13px] font-medium', brand.textActive)}
        >
          <Loader2 size={14} aria-hidden className="animate-spin" />
          <span>{statusText}</span>
        </div>
        <span
          aria-live="polite"
          role="status"
          className="inline-flex items-center gap-2 text-[11px] text-muted-foreground"
        >
          <span className="font-mono tabular-nums">{elapsed} elapsed</span>
          {percent !== null && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <span className="font-mono tabular-nums">{percent}%</span>
            </>
          )}
        </span>
        <div className="ml-auto flex items-center gap-2">
          {consoleUrl && (
            <a
              href={consoleUrl}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={`Open in ${providerLabel}`}
              className="inline-flex items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-[11px] text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
            >
              Open in {providerLabel}
              <ExternalLink size={11} aria-hidden />
            </a>
          )}
          {running.canCancel && !running.cancelLocked && (
            <Tooltip>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation();
                    void running.onCancel();
                  }}
                  disabled={running.cancelPending}
                  aria-label="Cancel query"
                  aria-busy={running.cancelPending}
                  className="inline-flex h-7 w-7 items-center justify-center rounded-md border border-border bg-background text-muted-foreground/70 transition-colors hover:bg-destructive/10 hover:text-destructive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 disabled:opacity-60"
                >
                  <Square size={12} aria-hidden fill="currentColor" />
                </button>
              </TooltipTrigger>
              <TooltipContent>Cancel query</TooltipContent>
            </Tooltip>
          )}
        </div>
      </div>

      <div
        className={cn('h-1.5 w-full overflow-hidden rounded-full', brand.progressTrack)}
        role="progressbar"
        aria-label="Query progress"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent === null ? undefined : percent}
      >
        {showShimmer ? (
          <div
            className={cn(
              'h-full w-1/3 rounded-full animate-[shimmer_1.5s_ease-in-out_infinite]',
              brand.progressFill,
            )}
          />
        ) : (
          <div
            className={cn('h-full rounded-full transition-all duration-700 ease-out', brand.progressFill)}
            style={{ width: `${percent}%` }}
          />
        )}
      </div>

      {errorContext.errorMessage && (
        <p className="text-[11px] text-destructive" role="alert">
          {errorContext.errorMessage}
        </p>
      )}

      {healthWarning && (
        <p className="text-[11px] text-amber-500" role="status" aria-live="polite">
          {healthWarning}
        </p>
      )}

      <div>
        <div className="mb-2 text-xs text-muted-foreground/60">Results</div>
        <div className="flex min-h-[120px] items-center justify-center rounded-md border border-dashed border-border/80 bg-muted/30 px-4 py-6 text-center">
          <p className="text-[13px] leading-[1.55] text-muted-foreground/90">
            {resultMessage}
          </p>
        </div>
      </div>

      {/* Telemetry strip — provider-specific tokens. Each token degrades
          silently when the source field is missing, so Snowflake/Trino don't
          render BigQuery-only chips. */}
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
        {stats.host && <Stat label={`host ${shortId(stats.host)}`} mono />}
        {stats.projectId && <Stat label={`project ${stats.projectId}`} mono />}
        {stats.bytesProcessed && <Stat label={`${stats.bytesProcessed} processed`} mono />}
        {stats.profilePhase && <Stat label={`profile ${stats.profilePhase}`} />}
        {stats.profileOutcome && <Stat label={`profile ${stats.profileOutcome}`} />}
        {stats.profileEstimatedBytes && (
          <Stat label={`${stats.profileEstimatedBytes} estimated${stats.profileEstimateIsExact ? ' (exact)' : ''}`} mono />
        )}
        {stats.remoteSubmitted === false && <Stat label="not submitted remotely" />}
        {stats.queuedElapsedMs !== null && (
          <Stat label={`${formatElapsedColon(stats.queuedElapsedMs)} queued`} mono />
        )}
        {stats.executionElapsedMs !== null && (
          <Stat label={`${formatElapsedColon(stats.executionElapsedMs)} executing`} mono />
        )}
        {stats.telemetryAgeMs !== null && (
          <Stat label={`telemetry ${formatElapsedColon(stats.telemetryAgeMs)} old`} mono />
        )}
        {stats.materializationState && stats.materializationState !== 'none' && (
          <Stat label={`results ${stats.materializationState}`} />
        )}
        {stats.validationState && <Stat label={`validation ${stats.validationState}`} />}
        {stats.resultTruncated && <Stat label="result capped" />}
      </div>

      {running.cancelLocked && phase !== 'cancelled' && (
        <p className="text-[11px] text-muted-foreground" role="status" aria-live="polite">
          {running.cancelPending ? 'Cancellation requested…' : 'Cancelled by user.'}
        </p>
      )}
    </div>
  );
}
