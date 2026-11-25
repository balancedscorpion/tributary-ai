import { expect, test } from '@playwright/test';

import type { TokenUsageBucket, TokenUsageReport } from '../../services/tokenUsageApi';
import {
  applyFilters,
  buildChart,
  buildTableRows,
  deriveFilterOptions,
  emptyCounts,
  emptyFilters,
  formatChartPoint,
} from '../../components/settings/usage/usageViewModel';

function usageBucket({
  model,
  totalTokens,
  date = '2026-07-12',
  overrides = {},
}: {
  model: string;
  totalTokens: number | null;
  date?: string;
  overrides?: Partial<TokenUsageBucket>;
}): TokenUsageBucket {
  return {
    ...emptyCounts(),
    date,
    providerId: 'test-provider',
    connectionId: 'test-connection',
    profileId: null,
    model,
    operation: 'chat',
    plannedApiSurface: 'test-surface',
    effectiveApiSurface: 'test-surface',
    totalTokens,
    ...overrides,
  };
}

test('keeps the five largest chart groups and deterministically folds the rest into Other', () => {
  const buckets = [
    usageBucket({ model: 'alpha', totalTokens: 100 }),
    usageBucket({ model: 'bravo', totalTokens: 90 }),
    usageBucket({ model: 'charlie', totalTokens: 80 }),
    usageBucket({ model: 'delta', totalTokens: 70 }),
    usageBucket({ model: 'echo', totalTokens: 60 }),
    usageBucket({ model: 'foxtrot', totalTokens: 50 }),
    usageBucket({ model: 'golf', totalTokens: 40 }),
  ];

  const chart = buildChart(buckets, 'model');

  expect(chart.series.map(({ label }) => label)).toEqual([
    'alpha',
    'bravo',
    'charlie',
    'delta',
    'echo',
    'All other models',
  ]);
  expect(chart.rows).toHaveLength(1);
  expect(chart.rows[0]).toMatchObject({
    date: '2026-07-12',
    series_0: 100,
    series_1: 90,
    series_2: 80,
    series_3: 70,
    series_4: 60,
    series_other: 90,
  });
  expect(buildChart(buckets, 'model')).toEqual(chart);
});

test('preserves exact, lower-bound, unknown, and incomplete-report chart semantics', () => {
  const exact = usageBucket({
    model: 'exact',
    totalTokens: 100,
    overrides: {
      invocationCount: 1,
      measuredInvocationCount: 1,
      totalMeasuredInvocationCount: 1,
    },
  });
  const floor = usageBucket({
    model: 'floor',
    totalTokens: null,
    overrides: {
      invocationCount: 1,
      lowerBoundInvocationCount: 1,
      lowerBoundTotalTokens: 40,
      lowerBoundTotalInvocationCount: 1,
    },
  });
  const unknown = usageBucket({
    model: 'unknown',
    totalTokens: null,
    overrides: { invocationCount: 1, missingUsageCount: 1 },
  });

  const chart = buildChart([exact, floor, unknown], 'model', true);
  const point = (label: string) => {
    const key = chart.series.find((series) => series.label === label)?.key;
    expect(key, `missing chart series for ${label}`).toBeTruthy();
    return {
      value: chart.rows[0][key!],
      meta: chart.metaByDate['2026-07-12'][key!],
      formatted: formatChartPoint(chart, Number(chart.rows[0][key!]), '2026-07-12', key!),
    };
  };

  expect(point('exact')).toEqual({ value: 100, meta: { partial: false, unknown: false }, formatted: '100' });
  expect(point('floor')).toEqual({ value: 40, meta: { partial: true, unknown: false }, formatted: '≥40' });
  expect(point('unknown')).toEqual({ value: 0, meta: { partial: false, unknown: true }, formatted: '—' });
  expect(chart.hasPartial).toBe(true);
  expect(chart.hasUnknown).toBe(true);

  const incomplete = buildChart([exact], 'model', false);
  expect(incomplete.metaByDate['2026-07-12'][incomplete.series[0].key])
    .toEqual({ partial: true, unknown: false });
});

test('keeps canonical chart groups separate when their display labels collide', () => {
  const chart = buildChart([
    usageBucket({ model: 'same-model', totalTokens: 100, overrides: { operation: 'provider_test' } }),
    usageBucket({ model: 'same-model', totalTokens: 200, overrides: { operation: 'provider-test' } }),
  ], 'operation');

  expect(chart.series).toHaveLength(2);
  expect(new Set(chart.series.map(({ key }) => key)).size).toBe(2);
  expect(new Set(chart.series.map(({ label }) => label)).size).toBe(2);
  expect(chart.series.every(({ label }) => label.startsWith('Provider Test · '))).toBe(true);
  expect(chart.series.map(({ key }) => chart.rows[0][key]).sort()).toEqual([100, 200]);
});

test('keeps literal backend values distinct from the all-filters state', () => {
  const literalSentinel = usageBucket({ model: '__all__', totalTokens: 100 });
  const other = usageBucket({ model: 'other-model', totalTokens: 200 });

  expect(applyFilters(
    [literalSentinel, other],
    { ...emptyFilters(), model: '__all__' },
  )).toEqual([literalSentinel]);
  expect(applyFilters([literalSentinel, other], emptyFilters())).toEqual([literalSentinel, other]);
});

test('disambiguates colliding connection, operation, and API-surface filter labels', () => {
  const options = deriveFilterOptions({
    buckets: [
      usageBucket({
        model: 'first',
        totalTokens: 100,
        overrides: {
          operation: 'provider_test',
          connectionId: 'Legacy: team',
          plannedApiSurface: 'responses',
          effectiveApiSurface: null,
        },
      }),
      usageBucket({
        model: 'second',
        totalTokens: 200,
        overrides: {
          operation: 'provider-test',
          connectionId: null,
          profileId: 'team',
          plannedApiSurface: 'responses',
          effectiveApiSurface: 'responses',
        },
      }),
    ],
  } as TokenUsageReport, null);

  for (const entries of [options.connections, options.operations, options.surfaces]) {
    expect(entries).toHaveLength(2);
    expect(new Set(entries.map(([, label]) => label)).size).toBe(2);
  }
  expect(options.connections.every(([, label]) => label.startsWith('Legacy: team · '))).toBe(true);
  expect(options.operations.every(([, label]) => label.startsWith('Provider Test · '))).toBe(true);
  expect(options.surfaces.every(([, label]) => label.startsWith('responses · '))).toBe(true);
});

test('assigns deterministic, collision-proof IDs to duplicate table buckets', () => {
  const duplicate = usageBucket({ model: 'duplicate-model', totalTokens: 100 });
  const buckets = [duplicate, { ...duplicate }, { ...duplicate }];

  const firstPass = buildTableRows(buckets).map(({ id }) => id);
  const secondPass = buildTableRows(buckets).map(({ id }) => id);

  expect(new Set(firstPass).size).toBe(3);
  expect(firstPass).toEqual(secondPass);
  expect(firstPass[0]).toMatch(/#0$/);
  expect(firstPass[1]).toMatch(/#1$/);
  expect(firstPass[2]).toMatch(/#2$/);
});

test('keeps row IDs attached to dimension-identical buckets when input order changes', () => {
  const smaller = usageBucket({ model: 'same-dimensions', totalTokens: 100 });
  const larger = usageBucket({ model: 'same-dimensions', totalTokens: 200 });
  const idsByTotal = (buckets: TokenUsageBucket[]) => Object.fromEntries(
    buildTableRows(buckets).map(({ id, bucket }) => [String(bucket.totalTokens), id]),
  );

  expect(idsByTotal([smaller, larger])).toEqual(idsByTotal([larger, smaller]));
});
