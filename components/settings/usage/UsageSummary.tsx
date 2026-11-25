import React, { useState } from 'react';
import { ChevronRight } from 'lucide-react';
import { Card, CardContent } from '@/components/ui/card';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import { cn } from '@/lib/utils';
import type { TokenUsageCounts } from '@/services/tokenUsageApi';
import { formatAllocatedShare, formatCount, formatTokenFloor } from './usageFormat';
import { buildTokenComposition, type TokenComposition } from './usageViewModel';
import { TokenFloorValue } from './TokenFloorValue';

/**
 * One-whole-split encoding, not two categories: the total is a single
 * quantity, so input carries the primary hue, output a light neutral, and the
 * non-series "not itemized" remainder the same faint gray the chart reserves
 * for "Other". This also keeps the hero from echoing the chart's first two
 * categorical series colors (#ea580c/#3b82f6) with a different meaning.
 * Contrast vs the dark card surface (#0e0e0e): all three >= 3:1.
 */
const COMPOSITION_COLORS = {
  input: '#ea580c',
  output: '#cbd5e1',
  unattributed: '#6b7280',
} as const;

interface CompositionPart {
  key: keyof typeof COMPOSITION_COLORS;
  label: string;
  /** Floor value used for segment width; null renders a keyed "—" only. */
  value: number | null;
  display: string;
  /** Largest-remainder integer share of the denominator; null when no value. */
  share: number | null;
  testId?: string;
  /** Coverage tooltip matching the table's per-cell pattern. */
  title?: string;
  /** Muted qualifier rendered under the value, e.g. cached/reasoning. */
  subDetail?: string;
}

function compositionParts(
  totals: TokenUsageCounts,
  composition: TokenComposition,
  reportComplete: boolean,
): CompositionPart[] {
  const cachedInput = formatTokenFloor(totals.cachedInputTokens, totals.lowerBoundCachedInputTokens, totals.cachedInputMeasuredInvocationCount, totals.lowerBoundCachedInputInvocationCount, totals.invocationCount, reportComplete);
  const reasoning = formatTokenFloor(totals.reasoningTokens, totals.lowerBoundReasoningTokens, totals.reasoningMeasuredInvocationCount, totals.lowerBoundReasoningInvocationCount, totals.invocationCount, reportComplete);
  const parts: CompositionPart[] = [
    {
      key: 'input',
      label: 'Input',
      value: composition.input,
      share: composition.shares.input,
      testId: 'usage-stat-input-tokens',
      display: formatTokenFloor(totals.inputTokens, totals.lowerBoundInputTokens, totals.inputMeasuredInvocationCount, totals.lowerBoundInputInvocationCount, totals.invocationCount, reportComplete),
      title: `${formatCount(totals.inputMeasuredInvocationCount)} exact and ${formatCount(totals.lowerBoundInputInvocationCount)} lower-bound input reports`,
      subDetail: cachedInput !== '—' ? `${cachedInput} cached` : undefined,
    },
    {
      key: 'output',
      label: 'Output',
      value: composition.output,
      share: composition.shares.output,
      testId: 'usage-stat-output-tokens',
      display: formatTokenFloor(totals.outputTokens, totals.lowerBoundOutputTokens, totals.outputMeasuredInvocationCount, totals.lowerBoundOutputInvocationCount, totals.invocationCount, reportComplete),
      title: `${formatCount(totals.outputMeasuredInvocationCount)} exact and ${formatCount(totals.lowerBoundOutputInvocationCount)} lower-bound output reports`,
      subDetail: reasoning !== '—' ? `${reasoning} reasoning` : undefined,
    },
  ];
  if (composition.unattributed > 0) {
    parts.push({
      key: 'unattributed',
      label: 'Not itemized',
      value: composition.unattributed,
      share: composition.shares.unattributed,
      display: formatCount(composition.unattributed),
      subDetail: 'no input/output split reported',
    });
  }
  return parts;
}

interface SecondaryMetric {
  label: string;
  /** Explicit because the hero context line already owns usage-stat-model-calls. */
  testId: string;
  value: string;
  detail: string;
}

/**
 * One hero figure (the recorded total), an input/output composition bar with
 * a textual key, and a default-closed "More metrics" disclosure carrying the
 * call, cache, reasoning, and coverage diagnostics.
 */
export function UsageSummary({
  totals,
  reportComplete,
  rangeLabel,
}: {
  totals: TokenUsageCounts;
  reportComplete: boolean;
  rangeLabel: string | null;
}) {
  const [moreOpen, setMoreOpen] = useState(false);
  const composition = buildTokenComposition(totals);
  const parts = composition ? compositionParts(totals, composition, reportComplete) : [];
  const nonzeroPartCount = parts.filter((part) => (part.value || 0) > 0).length;
  const showShares = Boolean(composition && !composition.contradictsTotal);

  const totalDisplay = formatTokenFloor(totals.totalTokens, totals.lowerBoundTotalTokens, totals.totalMeasuredInvocationCount, totals.lowerBoundTotalInvocationCount, totals.invocationCount, reportComplete);
  const callsDisplay = reportComplete ? formatCount(totals.invocationCount) : `≥${formatCount(totals.invocationCount)}`;

  const secondaryMetrics: SecondaryMetric[] = [
    {
      label: 'Model calls',
      testId: 'usage-stat-model-calls-detail',
      value: callsDisplay,
      detail: `${formatCount(totals.totalMeasuredInvocationCount)} exact · ${formatCount(totals.lowerBoundTotalInvocationCount)} lower-bound · ${formatCount(totals.missingUsageCount)} missing`,
    },
    {
      label: 'Cached input',
      testId: 'usage-stat-cached-input',
      value: formatTokenFloor(totals.cachedInputTokens, totals.lowerBoundCachedInputTokens, totals.cachedInputMeasuredInvocationCount, totals.lowerBoundCachedInputInvocationCount, totals.invocationCount, reportComplete),
      detail: `${formatCount(totals.cachedInputMeasuredInvocationCount)} exact · ${formatCount(totals.lowerBoundCachedInputInvocationCount)} lower-bound`,
    },
    {
      label: 'Cache writes',
      testId: 'usage-stat-cache-writes',
      value: formatTokenFloor(totals.cacheWriteTokens, totals.lowerBoundCacheWriteTokens, totals.cacheWriteMeasuredInvocationCount, totals.lowerBoundCacheWriteInvocationCount, totals.invocationCount, reportComplete),
      detail: `${formatCount(totals.cacheWriteMeasuredInvocationCount)} exact · ${formatCount(totals.lowerBoundCacheWriteInvocationCount)} lower-bound`,
    },
    {
      label: 'Reasoning tokens',
      testId: 'usage-stat-reasoning-tokens',
      value: formatTokenFloor(totals.reasoningTokens, totals.lowerBoundReasoningTokens, totals.reasoningMeasuredInvocationCount, totals.lowerBoundReasoningInvocationCount, totals.invocationCount, reportComplete),
      detail: `${formatCount(totals.reasoningMeasuredInvocationCount)} exact · ${formatCount(totals.lowerBoundReasoningInvocationCount)} lower-bound`,
    },
    {
      label: 'Upstream requests',
      testId: 'usage-stat-upstream-requests',
      value: reportComplete ? formatCount(totals.upstreamRequestCount) : `≥${formatCount(totals.upstreamRequestCount)}`,
      detail: 'HTTP requests behind the logical model calls',
    },
  ];

  return (
    <Card className="shadow-sm" data-testid="usage-primary-stats">
      <CardContent className="p-4 sm:p-5">
        <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5">
          <h3 className="text-sm font-medium text-muted-foreground">Total tokens</h3>
          {rangeLabel && <span className="text-xs text-muted-foreground">{rangeLabel}</span>}
        </div>
        <p className="mt-1.5 text-4xl font-semibold leading-none tracking-tight sm:text-5xl">
          <TokenFloorValue display={totalDisplay} testId="usage-stat-total-tokens" />
        </p>
        <p className="mt-2 text-sm text-muted-foreground">
          across{' '}
          <TokenFloorValue
            display={callsDisplay}
            testId="usage-stat-model-calls"
            className="font-medium text-foreground"
          />
          {' '}model {totals.invocationCount === 1 && reportComplete ? 'call' : 'calls'}
        </p>

        {composition ? (
          <div className="mt-5" data-testid="usage-composition">
            {/* Decorative reinforcement of the key below; 2px gaps let the card
                surface separate the segments, flex-grow keeps proportions
                exact. Only parts whose share rounds to at least 1% get the
                sliver-protecting min width, so the bar never overstates a
                value the key reports as "<1%". */}
            <div className="flex h-2.5 w-full gap-0.5" aria-hidden data-testid="usage-composition-bar">
              {parts.filter((part) => (part.value || 0) > 0).map((part) => (
                <div
                  key={part.key}
                  className={cn(
                    'rounded-[1px] first:rounded-l-full last:rounded-r-full',
                    (part.share || 0) >= 1 && 'min-w-[3px]',
                  )}
                  style={{
                    backgroundColor: COMPOSITION_COLORS[part.key],
                    flexGrow: part.value || 0,
                    flexBasis: 0,
                  }}
                />
              ))}
            </div>
            <dl className="mt-3 flex flex-wrap gap-x-6 gap-y-2">
              {parts.map((part) => (
                <div key={part.key} className="flex min-w-[8rem] items-start gap-2" data-testid={`usage-composition-${part.key}`}>
                  <span
                    aria-hidden
                    className="mt-1 h-2.5 w-2.5 shrink-0 rounded-sm"
                    style={{ backgroundColor: COMPOSITION_COLORS[part.key] }}
                  />
                  <div>
                    <dt className="text-xs text-muted-foreground">{part.label}</dt>
                    <dd className="mt-0.5 text-sm font-semibold" title={part.title}>
                      <TokenFloorValue display={part.display} testId={part.testId} />
                      {part.title && <span className="sr-only"> ({part.title})</span>}
                      {showShares && part.share !== null && part.value !== null && part.value > 0 && (
                        <span className="ml-1.5 text-xs font-normal text-muted-foreground">
                          {formatAllocatedShare(part.share, part.value, nonzeroPartCount > 1)}
                          <span className="sr-only"> of total</span>
                        </span>
                      )}
                      {part.subDetail && (
                        <span className="mt-0.5 block text-xs font-normal text-muted-foreground">{part.subDetail}</span>
                      )}
                    </dd>
                  </div>
                </div>
              ))}
            </dl>
            {composition.contradictsTotal && (
              <p className="mt-2 text-xs text-muted-foreground" data-testid="usage-composition-mismatch">
                Input and output are reported separately here and together exceed the recorded total, so shares of the total are not shown.
              </p>
            )}
          </div>
        ) : (
          <p className="mt-5 text-sm text-muted-foreground">
            No token counts were recorded for these calls.
          </p>
        )}

        <Collapsible open={moreOpen} onOpenChange={setMoreOpen} className="mt-5 border-t pt-1">
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="-mx-1 flex w-full items-center justify-between gap-3 rounded-md px-1 py-2 text-left text-sm font-medium hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              aria-label="More metrics"
              data-testid="usage-more-metrics-trigger"
            >
              <span className="flex items-center gap-2">
                <ChevronRight className={cn('h-4 w-4 text-muted-foreground transition-transform', moreOpen && 'rotate-90')} aria-hidden />
                More metrics
              </span>
              <span className="text-xs font-normal text-muted-foreground">Calls, cache, reasoning, and coverage</span>
            </button>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <dl className="grid gap-x-6 gap-y-3 px-1 pb-1 pt-2 sm:grid-cols-2 lg:grid-cols-3" data-testid="usage-more-metrics">
              {secondaryMetrics.map((metric) => (
                <div key={metric.label}>
                  <dt className="text-xs text-muted-foreground">{metric.label}</dt>
                  <dd className="mt-0.5 text-sm font-semibold tabular-nums">
                    <TokenFloorValue display={metric.value} testId={metric.testId} />
                    <span className="mt-0.5 block text-xs font-normal text-muted-foreground">{metric.detail}</span>
                  </dd>
                </div>
              ))}
            </dl>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
    </Card>
  );
}
