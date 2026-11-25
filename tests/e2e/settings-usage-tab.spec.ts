import { expect, test, type Locator, type Page, type Route } from '@playwright/test';

import { usagePayload } from './fixtures/usageReport';

async function mockUsage(page: Page, requests: string[]) {
  await page.route('**/api/usage/tokens/daily?**', async (route: Route) => {
    requests.push(route.request().url());
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(usagePayload(route.request().url())) });
  });
}

async function openAdvancedFilters(page: Page) {
  const trigger = page.getByRole('button', { name: /More filters/ });
  await trigger.click();
  await expect(trigger).toHaveAttribute('aria-expanded', 'true');
  return trigger;
}

async function closeAdvancedFilters(page: Page, trigger: Locator) {
  if (await trigger.getAttribute('aria-expanded') === 'true') {
    await page.keyboard.press('Escape');
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');
  }
}

test('accepts a backend-shaped empty report without converting unknown counters to zero', async ({ page }) => {
  await page.route('**/api/usage/tokens/daily?**', (route) => {
    const payload: any = usagePayload(route.request().url());
    payload.buckets = [];
    payload.tracking_started_at = null;
    const nullableCounters = [
      'input_tokens', 'output_tokens', 'provider_total_tokens', 'derived_total_tokens', 'total_tokens',
      'cached_input_tokens', 'cache_write_tokens', 'reasoning_tokens',
      'lower_bound_input_tokens', 'lower_bound_output_tokens', 'lower_bound_provider_total_tokens',
      'lower_bound_derived_total_tokens', 'lower_bound_total_tokens', 'lower_bound_cached_input_tokens',
      'lower_bound_cache_write_tokens', 'lower_bound_reasoning_tokens',
    ];
    for (const key of nullableCounters) payload.totals[key] = null;
    for (const key of Object.keys(payload.totals)) {
      if (!nullableCounters.includes(key)) payload.totals[key] = 0;
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(payload) });
  });
  await page.goto('/dev/usage', { waitUntil: 'domcontentloaded' });

  await expect(page.getByText('No token usage in this period')).toBeVisible();
  await expect(page.getByText('Token usage could not be loaded')).toHaveCount(0);
  await expect(page.getByTestId('usage-tracking-note')).toContainText('not deleted with a thread');
});

test('shows recorded usage, coverage, target dimensions, and bounded date-range requests', async ({ page }) => {
  const requests: string[] = [];
  await mockUsage(page, requests);
  await page.goto('/dev/usage', { waitUntil: 'domcontentloaded' });

  await expect(page.getByRole('heading', { name: 'Token usage' })).toBeVisible();
  await expect(page.getByTestId('usage-stat-total-tokens')).toHaveText('≥4,900');
  await expect(page.getByTestId('usage-stat-model-calls')).toHaveText('5');
  await expect(page.getByTestId('usage-stat-model-calls').locator('..'))
    .toContainText('across 5 model calls');

  // Input and output support the hero as a composition, not competing cards.
  await expect(page.getByTestId('usage-stat-input-tokens')).toHaveText('≥3,700');
  await expect(page.getByTestId('usage-composition-input')).toContainText('76%');
  await expect(page.getByTestId('usage-composition-input')).toContainText('≥600 cached');
  await expect(page.getByTestId('usage-stat-output-tokens')).toHaveText('≥1,200');
  await expect(page.getByTestId('usage-composition-output')).toContainText('24%');
  await expect(page.getByTestId('usage-composition-output')).toContainText('≥120 reasoning');
  // input + output fully cover the recorded total here, so no remainder key.
  await expect(page.getByTestId('usage-composition-unattributed')).toHaveCount(0);

  const moreMetrics = page.getByRole('button', { name: 'More metrics' });
  await expect(moreMetrics).toHaveAttribute('aria-expanded', 'false');
  await moreMetrics.click();
  await expect(moreMetrics).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByTestId('usage-stat-model-calls-detail')).toHaveText('5');
  await expect(page.getByTestId('usage-stat-model-calls-detail').locator('..'))
    .toContainText('4 exact · 0 lower-bound · 1 missing');
  await expect(page.getByTestId('usage-stat-cached-input')).toHaveText('≥600');
  await expect(page.getByTestId('usage-stat-cache-writes')).toHaveText('≥80');
  await expect(page.getByTestId('usage-stat-reasoning-tokens')).toHaveText('≥120');
  await expect(page.getByTestId('usage-stat-upstream-requests')).toHaveText('7');
  await page.getByRole('button', { name: 'Source and floor details' }).click();
  await expect(page.getByTestId('usage-quality-summary')).toContainText('3 provider-reported');
  await expect(page.getByTestId('usage-coverage-warning')).toContainText('1 of 5 model calls');
  await expect(page.getByRole('figure', { name: /daily recorded total tokens by model/i })).toBeVisible();
  await expect(page.getByTestId('usage-chart-qualifier'))
    .toContainText('for that series on that day');

  const table = page.getByTestId('usage-recorded-table');
  await expect(table.locator('tbody tr')).toHaveCount(3);
  await expect(table).toContainText('1,500');

  const bedrockRow = table.getByRole('row').filter({ hasText: 'claude-sonnet-4' });
  const bedrockDetails = bedrockRow.getByRole('button', { name: /details/i });
  await bedrockDetails.click();
  await expect(bedrockDetails).toHaveAttribute('aria-expanded', 'true');
  await expect(table).toContainText('Planned: bedrock-invoke-stream');
  await expect(table).toContainText('Effective: bedrock-converse-stream');
  await bedrockDetails.click();

  const legacyRow = table.getByRole('row').filter({ hasText: 'legacy-model' });
  const legacyDetails = legacyRow.getByRole('button', { name: /details/i });
  await legacyDetails.click();
  await expect(legacyDetails).toHaveAttribute('aria-expanded', 'true');
  await expect(table).toContainText('Legacy profile: legacy-compatible');
  await legacyDetails.click();

  await expect(page.getByTestId('usage-tracking-note')).toContainText('legacy run totals');
  await expect(page.getByTestId('usage-health-degraded')).toHaveCount(0);
  expect(new URL(requests[0]).searchParams.get('timezone')).toBeTruthy();

  const refreshBaseline = requests.length;
  await page.getByRole('button', { name: 'Refresh token usage' }).click();
  await expect.poll(() => requests.length).toBeGreaterThan(refreshBaseline);

  let advancedFilters = await openAdvancedFilters(page);
  await page.getByTestId('usage-operation-filter').click();
  await page.getByRole('option', { name: 'Provider Test' }).click();
  await closeAdvancedFilters(page, advancedFilters);
  await expect(page.getByTestId('usage-stat-total-tokens')).toHaveText('1,900');
  await expect(page.getByTestId('usage-recorded-table').locator('tbody tr')).toHaveCount(1);
  await expect(page.getByTestId('usage-filter-chip-operation')).toContainText('Provider Test');
  await expect(page.getByTestId('usage-more-filters-count')).toHaveText('1');
  await page.getByRole('button', { name: 'Remove Operation filter' }).click();
  await expect(page.getByTestId('usage-filter-chip-operation')).toHaveCount(0);

  advancedFilters = await openAdvancedFilters(page);
  await page.getByTestId('usage-surface-filter').click();
  await page.getByRole('option', { name: 'bedrock-invoke-stream → bedrock-converse-stream' }).click();
  await closeAdvancedFilters(page, advancedFilters);
  await expect(page.getByTestId('usage-recorded-table').locator('tbody tr')).toHaveCount(1);
  await page.getByRole('button', { name: 'Clear filters' }).click();

  advancedFilters = await openAdvancedFilters(page);
  await page.getByTestId('usage-connection-filter').click();
  await page.getByRole('option', { name: 'bedrock-primary' }).click();
  await closeAdvancedFilters(page, advancedFilters);
  await expect(page.getByTestId('usage-recorded-table').locator('tbody tr')).toHaveCount(1);
  await page.getByRole('button', { name: 'Clear filters' }).click();

  await page.getByTestId('usage-provider-filter').click();
  await page.getByRole('option', { name: 'openai' }).click();
  await expect(page.getByTestId('usage-stat-total-tokens')).toHaveText('3,000');
  await expect(page.getByTestId('usage-recorded-table').locator('tbody tr')).toHaveCount(1);
  await expect(page.getByTestId('usage-filter-chip-provider')).toContainText('openai');
  await expect(page.getByTestId('usage-coverage-warning')).toHaveCount(0);
  await expect(page.getByTestId('usage-quality-banner'))
    .toContainText('All 3 matching model calls have complete recorded total-token values.');

  advancedFilters = await openAdvancedFilters(page);
  await page.getByTestId('usage-operation-filter').click();
  await page.getByRole('option', { name: 'Provider Test' }).click();
  await closeAdvancedFilters(page, advancedFilters);
  await expect(page.getByTestId('usage-quality-banner'))
    .toContainText('No recorded model calls match the current filters.');
  await expect(page.getByText('No usage matches these filters')).toBeVisible();
  await expect(page.getByText(/Clear or change .* filters to see recorded usage/)).toBeVisible();
  await expect(page.getByText('No token usage in this period')).toHaveCount(0);
  await page.getByRole('button', { name: 'Clear filters' }).click();
  await expect(page.getByTestId('usage-coverage-warning')).toContainText('1 of 5 model calls');

  const presetBaseline = requests.length;
  await page.getByRole('button', { name: '7 days' }).click();
  await expect.poll(() => requests.length).toBeGreaterThan(presetBaseline);
  const sevenDayRequest = new URL(requests.at(-1)!);
  const from = new Date(`${sevenDayRequest.searchParams.get('from')}T12:00:00`);
  const to = new Date(`${sevenDayRequest.searchParams.get('to')}T12:00:00`);
  expect(Math.round((to.getTime() - from.getTime()) / 86_400_000)).toBe(6);

  await page.getByRole('button', { name: 'Custom' }).click();
  await page.getByLabel('From', { exact: true }).fill('2026-06-01');
  await page.getByLabel('To', { exact: true }).fill('2026-06-30');
  const customBaseline = requests.length;
  await page.getByRole('button', { name: 'Apply' }).click();
  await expect.poll(() => requests.length).toBeGreaterThan(customBaseline);
  const customRequest = new URL(requests.at(-1)!);
  expect(customRequest.searchParams.get('from')).toBe('2026-06-01');
  expect(customRequest.searchParams.get('to')).toBe('2026-06-30');

  await page.getByLabel('From', { exact: true }).fill('2024-01-01');
  await page.getByLabel('To', { exact: true }).fill('2026-06-30');
  const overLimitBaseline = requests.length;
  const customRangeError = page.getByText('Choose a date range of 366 days or fewer.');
  await expect(customRangeError).toBeVisible();
  for (const input of [
    page.getByLabel('From', { exact: true }),
    page.getByLabel('To', { exact: true }),
  ]) {
    await expect(input).toHaveAttribute('aria-invalid', 'true');
    const describedBy = await input.getAttribute('aria-describedby');
    expect(describedBy).toBeTruthy();
    await expect(page.locator(`[id="${describedBy}"]`))
      .toHaveText('Choose a date range of 366 days or fewer.');
  }
  await expect(page.getByRole('button', { name: 'Apply' })).toBeDisabled();
  expect(requests).toHaveLength(overLimitBaseline);

  await page.getByLabel('From', { exact: true }).fill('2026-06-01');
  for (const input of [
    page.getByLabel('From', { exact: true }),
    page.getByLabel('To', { exact: true }),
  ]) {
    await expect(input).not.toHaveAttribute('aria-invalid', 'true');
    await expect(input).not.toHaveAttribute('aria-describedby', /.+/);
  }
  await expect(page.getByRole('button', { name: 'Apply' })).toBeEnabled();
});

test('surfaces recorded totals that no input/output split accounts for', async ({ page }) => {
  await page.route('**/api/usage/tokens/daily?**', (route) => {
    const payload: any = usagePayload(route.request().url());
    // One call reported a bigger total than its input/output split accounts
    // for; totals must keep matching bucket sums for the strict parser.
    payload.buckets[0].provider_total_tokens += 500;
    payload.buckets[0].total_tokens += 500;
    payload.totals.provider_total_tokens += 500;
    payload.totals.total_tokens += 500;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(payload) });
  });
  await page.goto('/dev/usage', { waitUntil: 'domcontentloaded' });

  await expect(page.getByTestId('usage-stat-total-tokens')).toHaveText('≥5,400');
  const unattributed = page.getByTestId('usage-composition-unattributed');
  await expect(unattributed).toContainText('Not itemized');
  await expect(unattributed).toContainText('500');
  await expect(unattributed).toContainText('9%');
  await expect(page.getByTestId('usage-composition-bar').locator('div')).toHaveCount(3);
});

test('suppresses shares when recorded input and output exceed the recorded total', async ({ page }) => {
  await page.route('**/api/usage/tokens/daily?**', (route) => {
    const payload: any = usagePayload(route.request().url());
    // Mixed provider reporting: input/output sums legitimately exceed the
    // recorded total. Totals must keep matching bucket sums per counter.
    payload.buckets[0].input_tokens += 3_000;
    payload.totals.input_tokens += 3_000;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(payload) });
  });
  await page.goto('/dev/usage', { waitUntil: 'domcontentloaded' });

  await expect(page.getByTestId('usage-stat-total-tokens')).toHaveText('≥4,900');
  await expect(page.getByTestId('usage-stat-input-tokens')).toHaveText('≥6,700');
  await expect(page.getByTestId('usage-composition-mismatch'))
    .toContainText('together exceed the recorded total');
  await expect(page.getByTestId('usage-composition-input')).not.toContainText('%');
  await expect(page.getByTestId('usage-composition-output')).not.toContainText('%');
  await expect(page.getByTestId('usage-composition-unattributed')).toHaveCount(0);
});

test('distinguishes an unavailable usage endpoint from a transient error', async ({ page }) => {
  await page.route('**/api/usage/tokens/daily?**', (route) => route.fulfill({
    status: 404,
    contentType: 'application/json',
    body: JSON.stringify({ error: 'Usage reporting is not enabled.' }),
  }));
  await page.goto('/dev/usage', { waitUntil: 'domcontentloaded' });

  await expect(page.getByText('Usage reporting is unavailable')).toBeVisible();
  await expect(page.getByText('Usage reporting is not enabled.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Retry' })).toHaveCount(0);
});

test('surfaces transient failures and retries without reopening the tab', async ({ page }) => {
  let recovered = false;
  await page.route('**/api/usage/tokens/daily?**', (route) => {
    if (recovered) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(usagePayload(route.request().url())),
      });
    }
    return route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'Usage storage is temporarily unavailable.' }),
    });
  });
  await page.goto('/dev/usage', { waitUntil: 'domcontentloaded' });

  await expect(page.getByText('Token usage could not be loaded')).toBeVisible();
  await expect(page.getByText('Usage storage is temporarily unavailable.')).toBeVisible();
  recovered = true;
  await page.getByRole('button', { name: 'Retry' }).click();
  await expect(page.getByTestId('usage-stat-total-tokens')).toHaveText('≥4,900');
});

for (const status of ['degraded', 'unavailable'] as const) {
  test(`warns when usage tracking is ${status} and marks totals as lower bounds`, async ({ page }) => {
    await page.route('**/api/usage/tokens/daily?**', (route) => {
      const payload: any = usagePayload(route.request().url());
      payload.report_complete = false;
      payload.health = {
        status,
        message: status === 'degraded'
          ? 'One invocation could not be persisted.'
          : 'Usage storage was unavailable during part of this period.',
        dropped_invocation_count: 1,
        running_invocation_count: 2,
        stale_invocation_count: 1,
        pending_invocation_count: 1,
        terminal_pending_count: 0,
        quarantined_record_count: 0,
        oldest_pending_at: '2026-07-11T11:55:00Z',
      };
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(payload) });
    });
    await page.goto('/dev/usage', { waitUntil: 'domcontentloaded' });

    await expect(page.getByTestId(`usage-health-${status}`)).toBeVisible();
    await expect(page.getByTestId(`usage-health-${status}`)).toContainText('1 unrecorded');
    await expect(page.getByTestId('usage-stat-model-calls')).toHaveText('≥5');
    await expect(page.getByRole('heading', { name: 'Recorded usage details' })).toBeVisible();
    await expect(page.getByTestId('usage-recorded-table')).toContainText('≥3,000');
    await expect(page.getByTestId('usage-row-partial-badge')).toHaveCount(3);
    await expect(page.getByTestId('usage-row-partial-badge').first())
      .toHaveAttribute('title', 'This report is incomplete, so this row may omit usage');
    await expect(page.getByText('Exact usage')).toHaveCount(0);
  });
}

test('rejects malformed current-schema counts instead of displaying zero usage', async ({ page }) => {
  await page.route('**/api/usage/tokens/daily?**', (route) => {
    const payload: any = usagePayload(route.request().url());
    delete payload.totals.total_tokens;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(payload) });
  });
  await page.goto('/dev/usage', { waitUntil: 'domcontentloaded' });

  await expect(page.getByText('Token usage could not be loaded')).toBeVisible();
  await expect(page.getByText(/missing report totals total token count/i)).toBeVisible();
  await expect(page.getByTestId('usage-stat-total-tokens')).toHaveCount(0);
});
