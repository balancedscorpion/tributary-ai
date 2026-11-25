import type { TokenUsageBucket, TokenUsageCounts, TokenUsageReport } from '@/services/tokenUsageApi';
import {
  connectionFilterKey,
  connectionFilterLabel,
  formatCount,
  operationLabel,
  surfacePairKey,
  surfacePairLabel,
  tokenFloor,
} from './usageFormat';

export const ALL_FILTER: null = null;
export type UsageFilterValue = string | null;

export type UsageFilterDimension = 'provider' | 'model' | 'connection' | 'operation' | 'surface';

export interface UsageFilters {
  provider: UsageFilterValue;
  model: UsageFilterValue;
  connection: UsageFilterValue;
  operation: UsageFilterValue;
  surface: UsageFilterValue;
}

export function emptyFilters(): UsageFilters {
  return {
    provider: ALL_FILTER,
    model: ALL_FILTER,
    connection: ALL_FILTER,
    operation: ALL_FILTER,
    surface: ALL_FILTER,
  };
}

export const GROUP_BY_DIMENSIONS = [
  { value: 'model', label: 'Model' },
  { value: 'operation', label: 'Operation' },
  { value: 'provider', label: 'Provider' },
  { value: 'connection', label: 'Connection' },
  { value: 'surface', label: 'API surface' },
] as const;

export type GroupByDimension = (typeof GROUP_BY_DIMENSIONS)[number]['value'];

export function groupByLabel(dimension: GroupByDimension): string {
  return GROUP_BY_DIMENSIONS.find((entry) => entry.value === dimension)?.label || dimension;
}

export function emptyCounts(): TokenUsageCounts {
  return {
    inputTokens: null,
    outputTokens: null,
    providerTotalTokens: null,
    derivedTotalTokens: null,
    totalTokens: null,
    cachedInputTokens: null,
    cacheWriteTokens: null,
    reasoningTokens: null,
    lowerBoundInputTokens: null,
    lowerBoundOutputTokens: null,
    lowerBoundProviderTotalTokens: null,
    lowerBoundDerivedTotalTokens: null,
    lowerBoundTotalTokens: null,
    lowerBoundCachedInputTokens: null,
    lowerBoundCacheWriteTokens: null,
    lowerBoundReasoningTokens: null,
    invocationCount: 0,
    upstreamRequestCount: 0,
    measuredInvocationCount: 0,
    inputMeasuredInvocationCount: 0,
    outputMeasuredInvocationCount: 0,
    providerTotalMeasuredInvocationCount: 0,
    derivedTotalMeasuredInvocationCount: 0,
    totalMeasuredInvocationCount: 0,
    cachedInputMeasuredInvocationCount: 0,
    cacheWriteMeasuredInvocationCount: 0,
    reasoningMeasuredInvocationCount: 0,
    lowerBoundInvocationCount: 0,
    lowerBoundInputInvocationCount: 0,
    lowerBoundOutputInvocationCount: 0,
    lowerBoundProviderTotalInvocationCount: 0,
    lowerBoundDerivedTotalInvocationCount: 0,
    lowerBoundTotalInvocationCount: 0,
    lowerBoundCachedInputInvocationCount: 0,
    lowerBoundCacheWriteInvocationCount: 0,
    lowerBoundReasoningInvocationCount: 0,
    missingUsageCount: 0,
    failedCount: 0,
    cancelledCount: 0,
    interruptedCount: 0,
    providerReportedCount: 0,
    derivedUsageCount: 0,
    legacyPartialCount: 0,
  };
}

function addNullable(left: number | null, right: number | null): number | null {
  if (left === null) return right;
  if (right === null) return left;
  return left + right;
}

export function sumBuckets(buckets: TokenUsageBucket[]): TokenUsageCounts {
  return buckets.reduce<TokenUsageCounts>((sum, bucket) => ({
    inputTokens: addNullable(sum.inputTokens, bucket.inputTokens),
    outputTokens: addNullable(sum.outputTokens, bucket.outputTokens),
    providerTotalTokens: addNullable(sum.providerTotalTokens, bucket.providerTotalTokens),
    derivedTotalTokens: addNullable(sum.derivedTotalTokens, bucket.derivedTotalTokens),
    totalTokens: addNullable(sum.totalTokens, bucket.totalTokens),
    cachedInputTokens: addNullable(sum.cachedInputTokens, bucket.cachedInputTokens),
    cacheWriteTokens: addNullable(sum.cacheWriteTokens, bucket.cacheWriteTokens),
    reasoningTokens: addNullable(sum.reasoningTokens, bucket.reasoningTokens),
    lowerBoundInputTokens: addNullable(sum.lowerBoundInputTokens, bucket.lowerBoundInputTokens),
    lowerBoundOutputTokens: addNullable(sum.lowerBoundOutputTokens, bucket.lowerBoundOutputTokens),
    lowerBoundProviderTotalTokens: addNullable(sum.lowerBoundProviderTotalTokens, bucket.lowerBoundProviderTotalTokens),
    lowerBoundDerivedTotalTokens: addNullable(sum.lowerBoundDerivedTotalTokens, bucket.lowerBoundDerivedTotalTokens),
    lowerBoundTotalTokens: addNullable(sum.lowerBoundTotalTokens, bucket.lowerBoundTotalTokens),
    lowerBoundCachedInputTokens: addNullable(sum.lowerBoundCachedInputTokens, bucket.lowerBoundCachedInputTokens),
    lowerBoundCacheWriteTokens: addNullable(sum.lowerBoundCacheWriteTokens, bucket.lowerBoundCacheWriteTokens),
    lowerBoundReasoningTokens: addNullable(sum.lowerBoundReasoningTokens, bucket.lowerBoundReasoningTokens),
    invocationCount: sum.invocationCount + bucket.invocationCount,
    upstreamRequestCount: sum.upstreamRequestCount + bucket.upstreamRequestCount,
    measuredInvocationCount: sum.measuredInvocationCount + bucket.measuredInvocationCount,
    inputMeasuredInvocationCount: sum.inputMeasuredInvocationCount + bucket.inputMeasuredInvocationCount,
    outputMeasuredInvocationCount: sum.outputMeasuredInvocationCount + bucket.outputMeasuredInvocationCount,
    providerTotalMeasuredInvocationCount: sum.providerTotalMeasuredInvocationCount + bucket.providerTotalMeasuredInvocationCount,
    derivedTotalMeasuredInvocationCount: sum.derivedTotalMeasuredInvocationCount + bucket.derivedTotalMeasuredInvocationCount,
    totalMeasuredInvocationCount: sum.totalMeasuredInvocationCount + bucket.totalMeasuredInvocationCount,
    cachedInputMeasuredInvocationCount: sum.cachedInputMeasuredInvocationCount + bucket.cachedInputMeasuredInvocationCount,
    cacheWriteMeasuredInvocationCount: sum.cacheWriteMeasuredInvocationCount + bucket.cacheWriteMeasuredInvocationCount,
    reasoningMeasuredInvocationCount: sum.reasoningMeasuredInvocationCount + bucket.reasoningMeasuredInvocationCount,
    lowerBoundInvocationCount: sum.lowerBoundInvocationCount + bucket.lowerBoundInvocationCount,
    lowerBoundInputInvocationCount: sum.lowerBoundInputInvocationCount + bucket.lowerBoundInputInvocationCount,
    lowerBoundOutputInvocationCount: sum.lowerBoundOutputInvocationCount + bucket.lowerBoundOutputInvocationCount,
    lowerBoundProviderTotalInvocationCount: sum.lowerBoundProviderTotalInvocationCount + bucket.lowerBoundProviderTotalInvocationCount,
    lowerBoundDerivedTotalInvocationCount: sum.lowerBoundDerivedTotalInvocationCount + bucket.lowerBoundDerivedTotalInvocationCount,
    lowerBoundTotalInvocationCount: sum.lowerBoundTotalInvocationCount + bucket.lowerBoundTotalInvocationCount,
    lowerBoundCachedInputInvocationCount: sum.lowerBoundCachedInputInvocationCount + bucket.lowerBoundCachedInputInvocationCount,
    lowerBoundCacheWriteInvocationCount: sum.lowerBoundCacheWriteInvocationCount + bucket.lowerBoundCacheWriteInvocationCount,
    lowerBoundReasoningInvocationCount: sum.lowerBoundReasoningInvocationCount + bucket.lowerBoundReasoningInvocationCount,
    missingUsageCount: sum.missingUsageCount + bucket.missingUsageCount,
    failedCount: sum.failedCount + bucket.failedCount,
    cancelledCount: sum.cancelledCount + bucket.cancelledCount,
    interruptedCount: sum.interruptedCount + bucket.interruptedCount,
    providerReportedCount: sum.providerReportedCount + bucket.providerReportedCount,
    derivedUsageCount: sum.derivedUsageCount + bucket.derivedUsageCount,
    legacyPartialCount: sum.legacyPartialCount + bucket.legacyPartialCount,
  }), emptyCounts());
}

export interface UsageFilterOptions {
  providers: string[];
  models: string[];
  /** [filter key, display label] pairs. */
  connections: Array<[string, string]>;
  /** [raw operation, display label] pairs. */
  operations: Array<[string, string]>;
  /** [filter key, display label] pairs. */
  surfaces: Array<[string, string]>;
}

function connectionIdentityLabel(key: string): string {
  if (key.startsWith('connection:')) return `connection ${key.slice('connection:'.length)}`;
  if (key.startsWith('legacy:')) return `legacy profile ${key.slice('legacy:'.length)}`;
  return 'unbound';
}

function surfaceIdentityLabel(key: string): string {
  const [planned, effective] = JSON.parse(key) as [string | null, string | null];
  return `planned ${planned || 'unreported'}; effective ${effective || 'unreported'}`;
}

function disambiguateOptionLabels(
  entries: Array<[string, string]>,
  qualifier: (key: string) => string,
): Array<[string, string]> {
  const labelCounts = new Map<string, number>();
  for (const [, label] of entries) labelCounts.set(label, (labelCounts.get(label) || 0) + 1);
  return entries.map(([key, label]) => [
    key,
    (labelCounts.get(label) || 0) > 1 ? `${label} · ${qualifier(key)}` : label,
  ]);
}

export function deriveFilterOptions(report: TokenUsageReport | null, providerFilter: UsageFilterValue): UsageFilterOptions {
  const buckets = report?.buckets || [];
  const providers = Array.from(new Set(buckets.map((bucket) => bucket.providerId)))
    .sort((a, b) => a.localeCompare(b));
  const models = Array.from(new Set(
    buckets
      .filter((bucket) => providerFilter === ALL_FILTER || bucket.providerId === providerFilter)
      .map((bucket) => bucket.model),
  )).sort((a, b) => a.localeCompare(b));
  const connections = disambiguateOptionLabels(
    Array.from(new Map(
      buckets.map((bucket) => [connectionFilterKey(bucket), connectionFilterLabel(bucket)]),
    ).entries()),
    connectionIdentityLabel,
  ).sort((a, b) => a[1].localeCompare(b[1]));
  const operations = disambiguateOptionLabels(
    Array.from(new Set(buckets.map((bucket) => bucket.operation)))
      .map((operation): [string, string] => [operation, operationLabel(operation)]),
    (operation) => operation,
  ).sort((a, b) => a[1].localeCompare(b[1]));
  const surfaces = disambiguateOptionLabels(
    Array.from(new Map(
      buckets.map((bucket) => [
        surfacePairKey(bucket),
        surfacePairLabel(bucket.plannedApiSurface, bucket.effectiveApiSurface),
      ]),
    ).entries()),
    surfaceIdentityLabel,
  ).sort((a, b) => a[1].localeCompare(b[1]));
  return { providers, models, connections, operations, surfaces };
}

/**
 * Resets any filter whose value no longer exists in the loaded report.
 * Returns the same object when nothing changed so state updates can no-op.
 */
export function reconcileFilters(filters: UsageFilters, options: UsageFilterOptions): UsageFilters {
  const next: UsageFilters = { ...filters };
  let changed = false;
  if (filters.provider !== ALL_FILTER && !options.providers.includes(filters.provider)) {
    next.provider = ALL_FILTER;
    changed = true;
  }
  if (filters.model !== ALL_FILTER && !options.models.includes(filters.model)) {
    next.model = ALL_FILTER;
    changed = true;
  }
  if (filters.connection !== ALL_FILTER && !options.connections.some(([key]) => key === filters.connection)) {
    next.connection = ALL_FILTER;
    changed = true;
  }
  if (filters.operation !== ALL_FILTER && !options.operations.some(([key]) => key === filters.operation)) {
    next.operation = ALL_FILTER;
    changed = true;
  }
  if (filters.surface !== ALL_FILTER && !options.surfaces.some(([key]) => key === filters.surface)) {
    next.surface = ALL_FILTER;
    changed = true;
  }
  return changed ? next : filters;
}

export function isAnyFilterActive(filters: UsageFilters): boolean {
  return Object.values(filters).some((value) => value !== ALL_FILTER);
}

export function applyFilters(buckets: TokenUsageBucket[], filters: UsageFilters): TokenUsageBucket[] {
  return buckets.filter((bucket) => (
    (filters.provider === ALL_FILTER || bucket.providerId === filters.provider)
    && (filters.model === ALL_FILTER || bucket.model === filters.model)
    && (filters.connection === ALL_FILTER || connectionFilterKey(bucket) === filters.connection)
    && (filters.operation === ALL_FILTER || bucket.operation === filters.operation)
    && (filters.surface === ALL_FILTER || surfacePairKey(bucket) === filters.surface)
  ));
}

export function computeTotals(
  report: TokenUsageReport | null,
  filters: UsageFilters,
  visibleBuckets: TokenUsageBucket[],
): TokenUsageCounts {
  if (!report) return emptyCounts();
  // The unfiltered totals come straight from the server, whose validator
  // guarantees they equal the bucket sums; only recompute under a filter.
  if (!isAnyFilterActive(filters)) return report.totals;
  return sumBuckets(visibleBuckets);
}

function groupValue(bucket: TokenUsageBucket, groupBy: GroupByDimension): string {
  switch (groupBy) {
    case 'model':
      return bucket.model;
    case 'operation':
      return operationLabel(bucket.operation);
    case 'provider':
      return bucket.providerId;
    case 'connection':
      return connectionFilterLabel(bucket);
    case 'surface':
      return surfacePairLabel(bucket.plannedApiSurface, bucket.effectiveApiSurface);
    default:
      return bucket.model;
  }
}

/**
 * Canonical grouping identity, deliberately separate from the display label so
 * a group whose label happens to read "Other" (or a legacy profile that renders
 * like a connection id) can never merge with a different group or with the
 * fold-in series.
 */
function chartGroupKey(bucket: TokenUsageBucket, groupBy: GroupByDimension): string {
  switch (groupBy) {
    case 'operation':
      return bucket.operation;
    case 'provider':
      return bucket.providerId;
    case 'connection':
      return connectionFilterKey(bucket);
    case 'surface':
      return surfacePairKey(bucket);
    case 'model':
    default:
      return bucket.model;
  }
}

export const MAX_CHART_SERIES = 5;

export interface UsageChartSeries {
  key: string;
  label: string;
  isOther: boolean;
}

export interface UsageChartPointMeta {
  /** The stacked value is a floor: some contributing calls lack exact totals. */
  partial: boolean;
  /** No contributing call reported any total; the numeric 0 is not a measurement. */
  unknown: boolean;
}

export interface UsageChartData {
  series: UsageChartSeries[];
  rows: Array<Record<string, string | number>>;
  /** Per date, per series dataKey: floor/unknown qualifiers for that point. */
  metaByDate: Record<string, Record<string, UsageChartPointMeta>>;
  hasPartial: boolean;
  hasUnknown: boolean;
}

export function formatChartPoint(
  chart: UsageChartData,
  value: number,
  date: string,
  dataKey: string,
): string {
  const meta = chart.metaByDate[date]?.[dataKey];
  if (meta?.unknown) return '—';
  return `${meta?.partial ? '≥' : ''}${formatCount(value)}`;
}

function bucketTotalIsPartial(bucket: TokenUsageBucket, reportComplete: boolean): boolean {
  return !reportComplete
    || bucket.lowerBoundTotalInvocationCount > 0
    || bucket.totalMeasuredInvocationCount + bucket.lowerBoundTotalInvocationCount < bucket.invocationCount;
}

/**
 * Stacks daily recorded token floors by a single dimension. The five largest
 * groups (by range-wide floor total, ties by label then key) keep their own
 * series; everything else folds into a trailing "Other" series. Per-point
 * metadata records whether a value is a floor (≥) or entirely unreported (—)
 * so no consumer has to present the stacked 0 as an exact measurement.
 */
export function buildChart(
  visibleBuckets: TokenUsageBucket[],
  groupBy: GroupByDimension,
  reportComplete = true,
): UsageChartData {
  const groups = new Map<string, { total: number; label: string }>();
  for (const bucket of visibleBuckets) {
    const key = chartGroupKey(bucket, groupBy);
    const floor = tokenFloor(bucket.totalTokens, bucket.lowerBoundTotalTokens) || 0;
    const entry = groups.get(key) || { total: 0, label: groupValue(bucket, groupBy) };
    entry.total += floor;
    groups.set(key, entry);
  }
  const ranked = Array.from(groups.entries()).sort((a, b) => (
    b[1].total - a[1].total
      || a[1].label.localeCompare(b[1].label)
      || a[0].localeCompare(b[0])
  ));
  const top = ranked.slice(0, MAX_CHART_SERIES);
  const hasOther = ranked.length > MAX_CHART_SERIES;

  const topLabelCounts = new Map<string, number>();
  for (const [, group] of top) {
    topLabelCounts.set(group.label, (topLabelCounts.get(group.label) || 0) + 1);
  }

  const disambiguator = (key: string): string => {
    if (groupBy === 'operation') return key;
    if (groupBy === 'connection') return connectionIdentityLabel(key);
    if (groupBy === 'surface') return surfaceIdentityLabel(key);
    return key;
  };

  const series: UsageChartSeries[] = top.map(([key, group], index) => ({
    key: `series_${index}`,
    label: (topLabelCounts.get(group.label) || 0) > 1
      ? `${group.label} · ${disambiguator(key)}`
      : group.label,
    isOther: false,
  }));
  if (hasOther) {
    const otherLabels: Record<GroupByDimension, string> = {
      model: 'All other models',
      operation: 'All other operations',
      provider: 'All other providers',
      connection: 'All other connections',
      surface: 'All other API surfaces',
    };
    const baseLabel = otherLabels[groupBy];
    series.push({
      key: 'series_other',
      label: top.some(([, group]) => group.label === baseLabel) ? `${baseLabel} (combined)` : baseLabel,
      isOther: true,
    });
  }

  const seriesKeyByGroupKey = new Map(top.map(([key], index) => [key, `series_${index}`]));
  const byDate = new Map<string, Record<string, string | number>>();
  const pointState = new Map<string, { hasValue: boolean; partialAny: boolean }>();
  for (const bucket of visibleBuckets) {
    const seriesKey = seriesKeyByGroupKey.get(chartGroupKey(bucket, groupBy)) || 'series_other';
    const row = byDate.get(bucket.date) || { date: bucket.date };
    const floor = tokenFloor(bucket.totalTokens, bucket.lowerBoundTotalTokens);
    row[seriesKey] = Number(row[seriesKey] || 0) + (floor || 0);
    byDate.set(bucket.date, row);

    const stateKey = JSON.stringify([bucket.date, seriesKey]);
    const state = pointState.get(stateKey) || { hasValue: false, partialAny: false };
    if (floor !== null) state.hasValue = true;
    if (bucketTotalIsPartial(bucket, reportComplete)) state.partialAny = true;
    pointState.set(stateKey, state);
  }

  const metaByDate: Record<string, Record<string, UsageChartPointMeta>> = {};
  let hasPartial = false;
  let hasUnknown = false;
  for (const [stateKey, state] of pointState) {
    const [date, seriesKey] = JSON.parse(stateKey) as [string, string];
    const meta: UsageChartPointMeta = {
      partial: state.hasValue && state.partialAny,
      unknown: !state.hasValue,
    };
    hasPartial = hasPartial || meta.partial;
    hasUnknown = hasUnknown || meta.unknown;
    (metaByDate[date] = metaByDate[date] || {})[seriesKey] = meta;
  }

  return {
    series,
    rows: Array.from(byDate.values()).sort((a, b) => String(a.date).localeCompare(String(b.date))),
    metaByDate,
    hasPartial,
    hasUnknown,
  };
}

export interface TokenComposition {
  /**
   * Basis for shares: max(total floor, input floor + output floor). Mixed
   * per-call reporting means recorded input + output can legitimately land
   * under OR over the recorded total, so the wider of the two is the only
   * denominator that never yields a share above 100%.
   */
  denominator: number;
  /** Recorded floor per part; null means no call reported that counter. */
  input: number | null;
  output: number | null;
  /** Portion of the denominator no recorded input/output accounts for. */
  unattributed: number;
  /**
   * A recorded total exists AND recorded input + output exceed it, so any
   * percentage would be based on a number the headline does not show.
   * Consumers must suppress shares and explain instead.
   */
  contradictsTotal: boolean;
  /**
   * Integer percents by part key (null for parts with no value). Computed
   * with largest-remainder allocation so visible shares always sum to 100.
   */
  shares: { input: number | null; output: number | null; unattributed: number | null };
}

/**
 * Largest-remainder integer percentages: floors plus one extra point to the
 * biggest fractional remainders (ties broken by part order) until the floors
 * sum to 100. Naive per-part rounding can total 99 or 101, which reads as a
 * bug under a "Total" headline.
 */
function allocateShares(values: Array<number | null>, denominator: number): Array<number | null> {
  const exact = values.map((value) => (value === null ? null : (value / denominator) * 100));
  const floors = exact.map((value) => (value === null ? null : Math.floor(value)));
  let remaining = 100 - floors.reduce<number>((sum, value) => sum + (value || 0), 0);
  const order = exact
    .map((value, index) => ({ remainder: value === null ? -1 : value - (floors[index] || 0), index }))
    .filter((entry) => entry.remainder >= 0)
    .sort((a, b) => b.remainder - a.remainder || a.index - b.index);
  const allocated = [...floors];
  for (const entry of order) {
    if (remaining <= 0) break;
    allocated[entry.index] = (allocated[entry.index] || 0) + 1;
    remaining -= 1;
  }
  return allocated;
}

/**
 * Input/output decomposition of the recorded total for the composition bar.
 * Returns null when there is nothing to decompose (no token counters at all).
 */
export function buildTokenComposition(totals: TokenUsageCounts): TokenComposition | null {
  const input = tokenFloor(totals.inputTokens, totals.lowerBoundInputTokens);
  const output = tokenFloor(totals.outputTokens, totals.lowerBoundOutputTokens);
  const total = tokenFloor(totals.totalTokens, totals.lowerBoundTotalTokens);
  const attributed = (input || 0) + (output || 0);
  const denominator = Math.max(total || 0, attributed);
  if (denominator <= 0) return null;
  const unattributed = denominator - attributed;
  const contradictsTotal = total !== null && attributed > total;
  const [inputShare, outputShare, unattributedShare] = allocateShares(
    [input, output, unattributed > 0 ? unattributed : null],
    denominator,
  );
  return {
    denominator,
    input,
    output,
    unattributed,
    contradictsTotal,
    shares: { input: inputShare, output: outputShare, unattributed: unattributedShare },
  };
}

export interface UsageTableRow {
  /**
   * Deterministic and unique: dimension identity, a full-bucket count
   * fingerprint, and a duplicate-occurrence suffix. The fingerprint keeps
   * expansion state anchored to a row's data when dimension-identical rows
   * with different counts would otherwise reorder.
   */
  id: string;
  bucket: TokenUsageBucket;
}

export function bucketIsIncomplete(bucket: TokenUsageBucket, reportComplete = true): boolean {
  return !reportComplete || bucket.missingUsageCount > 0 || bucket.lowerBoundInvocationCount > 0;
}

/**
 * Buckets come from our own normalizer, which constructs every object with the
 * same key insertion order, so JSON serialization is a deterministic
 * full-bucket fingerprint.
 */
function bucketFingerprint(bucket: TokenUsageBucket): string {
  return JSON.stringify(bucket);
}

function hashString(value: string): string {
  let hash = 5381;
  for (let index = 0; index < value.length; index += 1) {
    hash = ((hash * 33) ^ value.charCodeAt(index)) >>> 0;
  }
  return hash.toString(36);
}

export function buildTableRows(visibleBuckets: TokenUsageBucket[]): UsageTableRow[] {
  const sorted = [...visibleBuckets].sort((a, b) => (
    b.date.localeCompare(a.date)
      || a.providerId.localeCompare(b.providerId)
      || a.model.localeCompare(b.model)
      || a.operation.localeCompare(b.operation)
      || (a.connectionId || '').localeCompare(b.connectionId || '')
      || (a.profileId || '').localeCompare(b.profileId || '')
      || surfacePairKey(a).localeCompare(surfacePairKey(b))
      || bucketFingerprint(a).localeCompare(bucketFingerprint(b))
  ));
  const occurrences = new Map<string, number>();
  return sorted.map((bucket) => {
    const identity = [
      bucket.date,
      bucket.providerId,
      bucket.connectionId || '',
      bucket.profileId || '',
      bucket.model,
      bucket.operation,
      surfacePairKey(bucket),
      hashString(bucketFingerprint(bucket)),
    ].join('\u0000');
    const occurrence = occurrences.get(identity) || 0;
    occurrences.set(identity, occurrence + 1);
    return { id: `${identity}\u0000#${occurrence}`, bucket };
  });
}
