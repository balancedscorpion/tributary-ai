import React, { useState } from 'react';
import { ChevronRight } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import type { TokenUsageBucket } from '@/services/tokenUsageApi';
import {
  formatCalendarDate,
  formatCount,
  formatTokenFloor,
  operationLabel,
  surfacePairLabel,
} from './usageFormat';
import { bucketIsIncomplete, type UsageTableRow } from './usageViewModel';
import { TokenFloorValue } from './TokenFloorValue';

function DetailMetric({
  label,
  value,
  title,
  emphasis,
}: {
  label: string;
  value: string;
  title?: string;
  emphasis?: boolean;
}) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd
        className={cn('mt-0.5 text-xs font-semibold tabular-nums', emphasis && 'text-amber-600 dark:text-amber-400')}
        title={title}
      >
        <TokenFloorValue display={value} />
        {title && <span className="sr-only"> ({title})</span>}
      </dd>
    </div>
  );
}

function RowDetails({
  bucket,
  detailId,
  reportComplete,
}: {
  bucket: TokenUsageBucket;
  detailId: string;
  reportComplete: boolean;
}) {
  return (
    <tr id={detailId} className="bg-muted/10" data-testid="usage-row-details">
      <td colSpan={7} className="px-3 py-3 pl-9">
        <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-2 lg:grid-cols-4">
          <DetailMetric
            label="Cached input"
            value={formatTokenFloor(bucket.cachedInputTokens, bucket.lowerBoundCachedInputTokens, bucket.cachedInputMeasuredInvocationCount, bucket.lowerBoundCachedInputInvocationCount, bucket.invocationCount, reportComplete)}
            title={`${formatCount(bucket.cachedInputMeasuredInvocationCount)} exact and ${formatCount(bucket.lowerBoundCachedInputInvocationCount)} lower-bound cached input reports`}
          />
          <DetailMetric
            label="Cache writes"
            value={formatTokenFloor(bucket.cacheWriteTokens, bucket.lowerBoundCacheWriteTokens, bucket.cacheWriteMeasuredInvocationCount, bucket.lowerBoundCacheWriteInvocationCount, bucket.invocationCount, reportComplete)}
            title={`${formatCount(bucket.cacheWriteMeasuredInvocationCount)} exact and ${formatCount(bucket.lowerBoundCacheWriteInvocationCount)} lower-bound cache write reports`}
          />
          <DetailMetric
            label="Reasoning"
            value={formatTokenFloor(bucket.reasoningTokens, bucket.lowerBoundReasoningTokens, bucket.reasoningMeasuredInvocationCount, bucket.lowerBoundReasoningInvocationCount, bucket.invocationCount, reportComplete)}
            title={`${formatCount(bucket.reasoningMeasuredInvocationCount)} exact and ${formatCount(bucket.lowerBoundReasoningInvocationCount)} lower-bound reasoning reports`}
          />
          <DetailMetric label="Upstream requests" value={formatCount(bucket.upstreamRequestCount)} />
          <DetailMetric
            label="Missing usage"
            value={formatCount(bucket.missingUsageCount)}
            emphasis={bucket.missingUsageCount > 0}
          />
          <DetailMetric label="Failed" value={formatCount(bucket.failedCount)} />
          <DetailMetric label="Cancelled" value={formatCount(bucket.cancelledCount)} />
          <DetailMetric label="Interrupted" value={formatCount(bucket.interruptedCount)} />
        </dl>
        <div className="mt-2.5 space-y-0.5 border-t pt-2 text-xs text-muted-foreground">
          {bucket.plannedApiSurface && bucket.effectiveApiSurface && bucket.plannedApiSurface !== bucket.effectiveApiSurface ? (
            <>
              <span className="block font-mono">Planned: {bucket.plannedApiSurface}</span>
              <span className="block font-mono">Effective: {bucket.effectiveApiSurface}</span>
            </>
          ) : (
            <span className="block font-mono">API surface: {surfacePairLabel(bucket.plannedApiSurface, bucket.effectiveApiSurface)}</span>
          )}
          {bucket.connectionId && <span className="block">Connection: {bucket.connectionId}</span>}
          {bucket.profileId && <span className="block">Legacy profile: {bucket.profileId}</span>}
        </div>
      </td>
    </tr>
  );
}

/**
 * Seven semantic columns; the disclosure button lives inside the Date cell so
 * the expansion row spans exactly the visible columns. Advanced dimensions
 * and reliability counters live in the per-row expansion.
 */
export function UsageTable({
  rows,
  reportComplete,
}: {
  rows: UsageTableRow[];
  reportComplete: boolean;
}) {
  const [expandedIds, setExpandedIds] = useState<ReadonlySet<string>>(new Set());

  const toggleRow = (id: string) => {
    setExpandedIds((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  return (
    <div className="rounded-lg border">
      <div className="border-b px-4 py-3">
        <h3 className="text-sm font-semibold">Recorded usage details</h3>
        <p className="mt-0.5 text-xs text-muted-foreground">
          Daily recorded token counts. ≥ marks a recorded floor and — marks an unreported counter; cache and reasoning categories depend on provider reporting. Expand a row for cache, reliability, and API surface details.
        </p>
      </div>
      {/* The one intentional horizontal scroller: focusable and named so
          keyboard users can scroll the wide table. */}
      <div
        className="max-w-full overflow-x-auto focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        data-testid="usage-table-scroll"
        tabIndex={0}
        role="region"
        aria-label="Recorded usage details table"
      >
        <table className="w-full min-w-[48rem] border-collapse text-left text-xs" data-testid="usage-recorded-table">
          <caption className="sr-only">Recorded daily token usage by provider, model, and operation, with per-row expansion for connection, API surface, and reliability details</caption>
          <thead className="bg-muted/40 text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-2 font-medium">Date</th>
              <th scope="col" className="px-3 py-2 font-medium">Model</th>
              <th scope="col" className="px-3 py-2 font-medium">Operation</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Input</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Output</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Total</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Calls</th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {rows.map(({ id, bucket }, index) => {
              const isOpen = expandedIds.has(id);
              const detailId = `usage-row-details-${index}`;
              const incomplete = bucketIsIncomplete(bucket, reportComplete);
              const selection = bucket.connectionId || bucket.profileId;
              const rowName = `${bucket.model} ${operationLabel(bucket.operation)} (${bucket.providerId}${selection ? ` · ${selection}` : ''}) on ${formatCalendarDate(bucket.date)}, row ${index + 1} of ${rows.length}`;
              return (
                <React.Fragment key={id}>
                  <tr className="hover:bg-muted/20">
                    <td className="whitespace-nowrap px-3 py-2.5">
                      <div className="flex items-center gap-1.5">
                        <button
                          type="button"
                          className="flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          aria-expanded={isOpen}
                          aria-controls={isOpen ? detailId : undefined}
                          aria-label={`${isOpen ? 'Hide' : 'Show'} details for ${rowName}`}
                          data-testid="usage-row-toggle"
                          onClick={() => toggleRow(id)}
                        >
                          <ChevronRight className={cn('h-3.5 w-3.5 transition-transform', isOpen && 'rotate-90')} aria-hidden />
                        </button>
                        <span>{formatCalendarDate(bucket.date)}</span>
                        {incomplete && (
                          <Badge
                            variant="outline"
                            className="border-amber-500/50 px-1.5 py-0 text-[11px] font-medium text-amber-600 dark:text-amber-400"
                            title={reportComplete
                              ? 'Some usage for this row is missing or reported only as a lower bound'
                              : 'This report is incomplete, so this row may omit usage'}
                            data-testid="usage-row-partial-badge"
                          >
                            Partial
                          </Badge>
                        )}
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <span className="block max-w-52 truncate font-mono" title={bucket.model}>{bucket.model}</span>
                      <span
                        className="block max-w-52 truncate text-xs text-muted-foreground"
                        title={`${bucket.providerId}${selection ? ` · ${selection}` : ''}`}
                      >
                        {bucket.providerId}{selection ? ` · ${selection}` : ''}
                      </span>
                    </td>
                    <td className="whitespace-nowrap px-3 py-2.5" title={bucket.operation}>{operationLabel(bucket.operation)}</td>
                    <td className="px-3 py-2.5 text-right tabular-nums" title={`${formatCount(bucket.inputMeasuredInvocationCount)} exact and ${formatCount(bucket.lowerBoundInputInvocationCount)} lower-bound input reports`}>
                      <TokenFloorValue display={formatTokenFloor(bucket.inputTokens, bucket.lowerBoundInputTokens, bucket.inputMeasuredInvocationCount, bucket.lowerBoundInputInvocationCount, bucket.invocationCount, reportComplete)} />
                    </td>
                    <td className="px-3 py-2.5 text-right tabular-nums" title={`${formatCount(bucket.outputMeasuredInvocationCount)} exact and ${formatCount(bucket.lowerBoundOutputInvocationCount)} lower-bound output reports`}>
                      <TokenFloorValue display={formatTokenFloor(bucket.outputTokens, bucket.lowerBoundOutputTokens, bucket.outputMeasuredInvocationCount, bucket.lowerBoundOutputInvocationCount, bucket.invocationCount, reportComplete)} />
                    </td>
                    <td className="px-3 py-2.5 text-right font-medium tabular-nums" title={`${formatCount(bucket.totalMeasuredInvocationCount)} exact and ${formatCount(bucket.lowerBoundTotalInvocationCount)} lower-bound total reports`}>
                      <TokenFloorValue display={formatTokenFloor(bucket.totalTokens, bucket.lowerBoundTotalTokens, bucket.totalMeasuredInvocationCount, bucket.lowerBoundTotalInvocationCount, bucket.invocationCount, reportComplete)} />
                    </td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{formatCount(bucket.invocationCount)}</td>
                  </tr>
                  {isOpen && (
                    <RowDetails bucket={bucket} detailId={detailId} reportComplete={reportComplete} />
                  )}
                </React.Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
