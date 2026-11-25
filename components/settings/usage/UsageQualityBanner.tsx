import React, { useState } from 'react';
import { AlertTriangle, ChevronRight, ShieldCheck } from 'lucide-react';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import { cn } from '@/lib/utils';
import type { TokenUsageCounts, TokenUsageHealth } from '@/services/tokenUsageApi';
import { formatCount } from './usageFormat';

type Severity = 'neutral' | 'warning' | 'critical';

function healthDetails(health: TokenUsageHealth): string {
  return [
    health.droppedInvocationCount > 0 ? `${formatCount(health.droppedInvocationCount)} unrecorded` : null,
    health.pendingInvocationCount > 0 ? `${formatCount(health.pendingInvocationCount)} pending persistence` : null,
    health.terminalPendingCount > 0 ? `${formatCount(health.terminalPendingCount)} completed and queued` : null,
    health.quarantinedRecordCount > 0 ? `${formatCount(health.quarantinedRecordCount)} quarantined` : null,
    health.runningInvocationCount > 0 ? `${formatCount(health.runningInvocationCount)} still running` : null,
    health.staleInvocationCount > 0 ? `${formatCount(health.staleInvocationCount)} awaiting reconciliation` : null,
  ].filter(Boolean).join(' · ');
}

/**
 * The single, always-present data-quality surface: a compact neutral line when
 * tracking is healthy and coverage is complete, severity-styled otherwise.
 * Source and floor details live behind one disclosure. Legacy markers
 * (usage-health-*, usage-coverage-warning) are preserved as nested testids.
 */
export function UsageQualityBanner({
  health,
  reportComplete,
  totals,
  filtersActive,
}: {
  health: TokenUsageHealth;
  reportComplete: boolean;
  /** Totals for the CURRENT view — already narrowed when filters are active. */
  totals: TokenUsageCounts;
  filtersActive: boolean;
}) {
  const [detailsOpen, setDetailsOpen] = useState(false);

  const healthIssue = health.status !== 'healthy' || !reportComplete;
  const coverageIssue = totals.missingUsageCount > 0 || totals.lowerBoundTotalInvocationCount > 0;
  const severity: Severity = health.status === 'unavailable'
    ? 'critical'
    : healthIssue || coverageIssue
      ? 'warning'
      : 'neutral';

  const title = health.status === 'unavailable'
    ? 'Usage tracking was unavailable'
    : health.status === 'degraded'
      ? 'Usage tracking is degraded'
      : !reportComplete
        ? 'This usage report is incomplete'
        : coverageIssue
          ? 'Some calls have incomplete provider usage'
          : 'Usage tracking is healthy';

  const healthLine = healthIssue
    ? `${health.message || 'Recorded token totals may omit one or more model invocations for this period.'}${healthDetails(health) ? ` ${healthDetails(health)}.` : ''}`
    : null;
  const coverageLine = coverageIssue
    ? `${formatCount(totals.missingUsageCount)} of ${formatCount(totals.invocationCount)} model calls have no total-token count; ${formatCount(totals.lowerBoundTotalInvocationCount)} have a partial lower-bound total. Token totals may be lower than actual consumption, and individual input/output sums can also be partial.`
    : null;
  const neutralLine = totals.invocationCount > 0
    ? filtersActive
      ? `All ${formatCount(totals.invocationCount)} matching model calls have complete recorded total-token values.`
      : `All ${formatCount(totals.invocationCount)} recorded model calls have complete recorded total-token values.`
    : filtersActive
      ? 'No recorded model calls match the current filters.'
      : 'No model calls have been recorded for this period yet.';

  const Icon = severity === 'neutral' ? ShieldCheck : AlertTriangle;

  return (
    /* Stable live-region container: assistive tech doesn't reliably
       re-register a node whose role flips status<->alert, so the role stays
       "status" and only aria-live urgency varies. Neutral severity renders as
       a quiet unboxed line; the filled box is reserved for real problems. */
    <div
      className={cn(
        'text-sm',
        severity === 'critical' && 'rounded-lg border border-destructive/40 bg-destructive/10 p-3',
        severity === 'warning' && 'rounded-lg border border-amber-500/40 bg-amber-500/10 p-3',
        severity === 'neutral' && 'px-1 py-0.5',
      )}
      role="status"
      aria-live={severity === 'critical' ? 'assertive' : 'polite'}
      data-testid="usage-quality-banner"
    >
      <div className="flex gap-3">
        <Icon
          className={cn(
            'mt-0.5 h-4 w-4 shrink-0',
            severity === 'critical' && 'text-destructive',
            severity === 'warning' && 'text-amber-600 dark:text-amber-400',
            severity === 'neutral' && 'text-muted-foreground',
          )}
          aria-hidden
        />
        <div className="min-w-0 flex-1">
          {severity === 'neutral' ? (
            <p className="text-sm text-muted-foreground">{neutralLine}</p>
          ) : (
            <p className="font-medium">{title}</p>
          )}
          {healthIssue ? (
            <div data-testid={`usage-health-${health.status}`}>
              <p className="mt-0.5 text-xs text-muted-foreground">{healthLine}</p>
            </div>
          ) : null}
          {coverageIssue ? (
            <div data-testid="usage-coverage-warning">
              <p className="mt-0.5 text-xs text-muted-foreground">{coverageLine}</p>
            </div>
          ) : null}

          <Collapsible open={detailsOpen} onOpenChange={setDetailsOpen}>
            <CollapsibleTrigger asChild>
              <button
                type="button"
                className="mt-1.5 inline-flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                data-testid="usage-quality-details-trigger"
              >
                <ChevronRight className={cn('h-3.5 w-3.5 transition-transform', detailsOpen && 'rotate-90')} aria-hidden />
                Source and floor details
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                <p data-testid="usage-quality-summary">
                  Usage sources: {formatCount(totals.providerReportedCount)} provider-reported · {formatCount(totals.derivedUsageCount)} derived totals · {formatCount(totals.legacyPartialCount)} partial/lower-bound
                </p>
                <p data-testid="usage-status-counts">
                  {formatCount(totals.upstreamRequestCount)} upstream requests · {formatCount(totals.failedCount)} failed · {formatCount(totals.cancelledCount)} cancelled · {formatCount(totals.interruptedCount)} interrupted
                </p>
                <p>
                  ≥ marks a floor: some calls reported only partial usage, so the true count is at least the value shown. — means no call reported that counter.
                </p>
              </div>
            </CollapsibleContent>
          </Collapsible>
        </div>
      </div>
    </div>
  );
}
