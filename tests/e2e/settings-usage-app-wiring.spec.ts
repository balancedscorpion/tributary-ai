import { expect, test, type Locator, type Page, type Route } from '@playwright/test';

import {
  usagePayload,
  usagePayloadWithDuplicateBuckets,
} from './fixtures/usageReport';

type ProductionUsageHarness = {
  requests: string[];
  unexpectedNonGetRequests: string[];
};

type OpenUsageResult = {
  settings: Locator;
  usage: Locator;
  viewport: Locator;
};

const productionViewports = [
  { width: 1280, height: 720 },
  { width: 1024, height: 768 },
  { width: 768, height: 900 },
] as const;

function jsonResponse(body: unknown, status = 200) {
  return {
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  };
}

async function installProductionUsageApi(
  page: Page,
  buildUsagePayload: typeof usagePayload = usagePayload,
): Promise<ProductionUsageHarness> {
  const requests: string[] = [];
  const unexpectedNonGetRequests: string[] = [];

  await page.route('**/api/**', async (route: Route) => {
    const request = route.request();
    const url = new URL(request.url());
    const method = request.method().toUpperCase();

    if (url.pathname === '/api/usage/tokens/daily' && method === 'GET') {
      requests.push(request.url());
      await route.fulfill(jsonResponse(buildUsagePayload(request.url())));
      return;
    }

    if (url.pathname === '/api/settings' && method === 'GET') {
      await route.fulfill(jsonResponse({ settings: {} }));
      return;
    }

    if (url.pathname === '/api/activation/status' && method === 'GET') {
      await route.fulfill(jsonResponse({ activated: true }));
      return;
    }

    if (url.pathname === '/api/secrets/status' && method === 'GET') {
      await route.fulfill(jsonResponse({
        initialized: true,
        locked: false,
        configured: {},
        present: {},
      }));
      return;
    }

    if (url.pathname === '/api/threads' && method === 'GET') {
      await route.fulfill(jsonResponse({ threads: [] }));
      return;
    }

    // The shell creates an initial untitled thread when the thread list is
    // empty. That startup write is expected and unrelated to the read-only
    // usage surface; keep it deterministic so genuinely unexpected writes
    // still fail the harness assertion below.
    if (url.pathname === '/api/threads' && method === 'POST') {
      await route.fulfill(jsonResponse({
        thread: {
          id: 'usage-settings-thread',
          title: 'Usage settings test',
          created_at: 1_720_000_000_000,
          updated_at: 1_720_000_000_000,
        },
      }));
      return;
    }

    // App startup has several best-effort read integrations. Keep those reads
    // deterministic without silently accepting an unexpected write.
    if (method !== 'GET') {
      unexpectedNonGetRequests.push(`${method} ${url.pathname}`);
      await route.fulfill(jsonResponse({ error: 'Unexpected write in usage test harness.' }, 405));
      return;
    }
    await route.fulfill(jsonResponse({}));
  });

  return { requests, unexpectedNonGetRequests };
}

async function openProductionUsage(
  page: Page,
  expectedTotal = '≥4,900',
): Promise<OpenUsageResult> {
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await page.getByRole('button', { name: 'Open settings' }).click();

  const settings = page.getByRole('dialog', { name: 'Settings' });
  await expect(settings).toBeVisible();

  // The dialog is visible while activation and secrets checks are still
  // resolving, so wait for the real tab list before attempting to navigate.
  const usageTab = settings.getByRole('tab', { name: 'Usage' });
  await expect(usageTab).toBeVisible();
  await usageTab.click();
  await expect(usageTab).toHaveAttribute('aria-selected', 'true');

  const usage = settings.getByTestId('settings-usage-tab');
  await expect(usage).toBeVisible();
  await expect(usage.getByTestId('usage-stat-total-tokens')).toHaveText(expectedTotal);

  const viewport = settings
    .getByTestId('settings-scroll-area')
    .locator('[data-radix-scroll-area-viewport]');
  await expect(viewport).toBeVisible();

  return { settings, usage, viewport };
}

async function expectNoOuterHorizontalOverflow(
  page: Page,
  { settings, usage, viewport }: OpenUsageResult,
) {
  const documentMetrics = await page.evaluate(() => ({
    viewportWidth: window.innerWidth,
    bodyClientWidth: document.body.clientWidth,
    bodyScrollWidth: document.body.scrollWidth,
    htmlClientWidth: document.documentElement.clientWidth,
    htmlScrollWidth: document.documentElement.scrollWidth,
  }));

  expect(documentMetrics.bodyScrollWidth, 'body must not horizontally scroll')
    .toBeLessThanOrEqual(documentMetrics.bodyClientWidth + 1);
  expect(documentMetrics.htmlScrollWidth, 'document must not horizontally scroll')
    .toBeLessThanOrEqual(documentMetrics.htmlClientWidth + 1);
  expect(documentMetrics.htmlScrollWidth, 'document must fit the browser viewport')
    .toBeLessThanOrEqual(documentMetrics.viewportWidth + 1);

  for (const [name, locator] of [
    ['Settings dialog', settings],
    ['Settings scroll viewport', viewport],
    ['Usage section', usage],
  ] as const) {
    const metrics = await locator.evaluate((element) => ({
      clientWidth: element.clientWidth,
      scrollWidth: element.scrollWidth,
    }));
    expect(metrics.clientWidth, `${name} must have a measurable width`).toBeGreaterThan(0);
    expect(metrics.scrollWidth, `${name} must own no horizontal overflow`)
      .toBeLessThanOrEqual(metrics.clientWidth + 1);
  }

  const viewportBox = await viewport.boundingBox();
  if (!viewportBox) throw new Error('Settings viewport has no bounding box');
  const viewportLeft = viewportBox.x;
  const viewportRight = viewportBox.x + viewportBox.width;

  const criticalBlocks = [
    ['usage controls', usage.getByTestId('usage-controls')],
    ['primary usage metrics', usage.getByTestId('usage-primary-stats')],
    ['usage composition', usage.getByTestId('usage-composition')],
    ['usage quality', usage.getByTestId('usage-quality-banner')],
    ['usage filters', usage.getByTestId('usage-filter-bar')],
    ['usage chart', usage.getByTestId('usage-chart')],
    ['usage table frame', usage.getByTestId('usage-table-scroll')],
    ['tracking note', usage.getByTestId('usage-tracking-note')],
  ] as const;

  for (const [name, locator] of criticalBlocks) {
    await expect(locator, `${name} should render`).toBeVisible();
    const box = await locator.boundingBox();
    if (!box) throw new Error(`${name} has no bounding box`);
    expect(box.x, `${name} must not be clipped on the left`)
      .toBeGreaterThanOrEqual(viewportLeft - 1);
    expect(box.x + box.width, `${name} must not be clipped on the right`)
      .toBeLessThanOrEqual(viewportRight + 1);

    if (name !== 'usage table frame') {
      const metrics = await locator.evaluate((element) => ({
        clientWidth: element.clientWidth,
        scrollWidth: element.scrollWidth,
      }));
      expect(metrics.clientWidth, `${name} must have a measurable width`).toBeGreaterThan(0);
      expect(metrics.scrollWidth, `${name} must not hide or own horizontal overflow`)
        .toBeLessThanOrEqual(metrics.clientWidth + 1);
    }
  }

  const tableScroller = usage.getByTestId('usage-table-scroll');
  await expect(tableScroller).toHaveAttribute('role', 'region');
  await expect(tableScroller).toHaveAttribute('tabindex', '0');
  await expect(tableScroller).toHaveAccessibleName(/usage/i);

  const tableMetrics = await tableScroller.evaluate((element) => ({
    clientWidth: element.clientWidth,
    scrollWidth: element.scrollWidth,
    overflowX: getComputedStyle(element).overflowX,
  }));
  expect(tableMetrics.clientWidth).toBeGreaterThan(0);
  if (documentMetrics.viewportWidth <= 768) {
    expect(tableMetrics.scrollWidth, 'compact layout must exercise the table-owned scroller')
      .toBeGreaterThan(tableMetrics.clientWidth);
  } else {
    expect(tableMetrics.scrollWidth).toBeGreaterThanOrEqual(tableMetrics.clientWidth);
  }
  expect(tableMetrics.overflowX).toBe('auto');
}

async function controlledRegion(toggle: Locator, page: Page): Promise<Locator> {
  const controls = await toggle.getAttribute('aria-controls');
  expect(controls, 'disclosure must identify its controlled region').toBeTruthy();
  return page.locator(`[id="${controls}"]`);
}

test.describe('production Settings usage wiring', () => {
  for (const viewportSize of productionViewports) {
    test(`contains the complete usage experience at ${viewportSize.width}x${viewportSize.height}`, async ({ page }) => {
      await page.setViewportSize(viewportSize);
      const harness = await installProductionUsageApi(page);
      const opened = await openProductionUsage(page);

      await expect(opened.usage.getByTestId('usage-stat-model-calls')).toHaveText('5');
      const primaryStats = opened.usage.getByTestId('usage-primary-stats');
      // The hero contract: one dominant total, with input/output demoted to
      // the composition treatment inside the same card.
      await expect(primaryStats.getByTestId('usage-stat-total-tokens')).toHaveText('≥4,900');
      await expect(primaryStats.getByTestId('usage-composition-bar')).toBeVisible();
      await expect(primaryStats.getByTestId('usage-stat-input-tokens')).toHaveText('≥3,700');
      await expect(primaryStats.getByTestId('usage-stat-output-tokens')).toHaveText('≥1,200');
      const heroFontSize = await primaryStats
        .getByTestId('usage-stat-total-tokens')
        .evaluate((element) => Number.parseFloat(getComputedStyle(element).fontSize));
      const supportingFontSize = await primaryStats
        .getByTestId('usage-stat-input-tokens')
        .evaluate((element) => Number.parseFloat(getComputedStyle(element).fontSize));
      expect(heroFontSize, 'total tokens must render as the dominant headline').toBeGreaterThanOrEqual(40);
      expect(heroFontSize).toBeGreaterThan(supportingFontSize * 2);
      await expect(opened.usage.getByTestId('usage-provider-filter')).toBeVisible();
      await expect(opened.usage.getByTestId('usage-model-filter')).toBeVisible();
      await expect(opened.usage.getByTestId('usage-quality-banner')).toHaveCount(1);
      await expect(opened.usage.getByRole('button', { name: 'Source and floor details' })).toHaveCount(1);
      await expect(opened.usage.getByLabel('Group usage by')).toBeVisible();
      await expect(opened.usage.getByRole('button', { name: 'More metrics' })).toBeVisible();
      await expect(opened.usage.getByRole('button', { name: /More filters/ })).toBeVisible();
      await expect(opened.usage.getByTestId('usage-recorded-table').locator('thead th')).toHaveText([
        'Date',
        'Model',
        'Operation',
        'Input',
        'Output',
        'Total',
        'Calls',
      ]);
      await expect.poll(() => harness.requests.length).toBeGreaterThan(0);
      expect(new URL(harness.requests[0]).searchParams.get('timezone')).toBeTruthy();

      await expectNoOuterHorizontalOverflow(page, opened);

      if (viewportSize.width === 768) {
        const moreMetrics = opened.usage.getByRole('button', { name: 'More metrics' });
        await moreMetrics.focus();
        await page.keyboard.press('Enter');
        await expect(moreMetrics).toHaveAttribute('aria-expanded', 'true');
        await expect(await controlledRegion(moreMetrics, page)).toBeVisible();
        await expect(moreMetrics).toBeFocused();
        await expectNoOuterHorizontalOverflow(page, opened);

        const moreFilters = opened.usage.getByRole('button', { name: /More filters/ });
        await moreFilters.focus();
        await page.keyboard.press('Enter');
        await expect(moreFilters).toHaveAttribute('aria-expanded', 'true');
        const filtersContent = await controlledRegion(moreFilters, page);
        await expect(filtersContent).toBeVisible();

        const filtersBox = await filtersContent.boundingBox();
        if (!filtersBox) throw new Error('More filters content has no bounding box');
        expect(filtersBox.x).toBeGreaterThanOrEqual(0);
        expect(filtersBox.x + filtersBox.width).toBeLessThanOrEqual(viewportSize.width + 1);

        await page.keyboard.press('Escape');
        await expect(moreFilters).toHaveAttribute('aria-expanded', 'false');
        await expect(moreFilters).toBeFocused();

        const detailToggle = opened.usage
          .getByTestId('usage-recorded-table')
          .getByRole('row')
          .filter({ hasText: 'claude-sonnet-4' })
          .getByRole('button', { name: /details/i });
        await detailToggle.focus();
        await page.keyboard.press('Enter');
        await expect(detailToggle).toHaveAttribute('aria-expanded', 'true');
        await expect(await controlledRegion(detailToggle, page)).toBeVisible();
        await expectNoOuterHorizontalOverflow(page, opened);
      }

      expect(harness.unexpectedNonGetRequests).toEqual([]);
    });
  }

  test('keeps advanced usage controls and row details keyboard-accessible', async ({ page }) => {
    const harness = await installProductionUsageApi(page);
    const opened = await openProductionUsage(page);

    const labelledBy = await opened.usage.getAttribute('aria-labelledby');
    expect(labelledBy).toBeTruthy();
    await expect(opened.settings.locator(`[id="${labelledBy}"]`)).toHaveText('Token usage');

    const tableScroller = opened.usage.getByRole('region', { name: 'Recorded usage details table' });
    await expect(tableScroller).toHaveAttribute('tabindex', '0');
    await tableScroller.focus();
    await expect(tableScroller).toBeFocused();

    const moreMetrics = opened.usage.getByRole('button', { name: 'More metrics' });
    await expect(moreMetrics).toHaveAttribute('aria-expanded', 'false');
    await moreMetrics.focus();
    await page.keyboard.press('Enter');
    await expect(moreMetrics).toHaveAttribute('aria-expanded', 'true');
    await expect(await controlledRegion(moreMetrics, page)).toBeVisible();
    await expect(moreMetrics).toBeFocused();
    await page.keyboard.press('Space');
    await expect(moreMetrics).toHaveAttribute('aria-expanded', 'false');

    const moreFilters = opened.usage.getByRole('button', { name: /More filters/ });
    await moreFilters.focus();
    await page.keyboard.press('Enter');
    await expect(moreFilters).toHaveAttribute('aria-expanded', 'true');
    const filtersContent = await controlledRegion(moreFilters, page);
    await expect(filtersContent).toBeVisible();
    await expect(filtersContent.getByLabel('Connection')).toBeVisible();
    await expect(filtersContent.getByLabel('Operation')).toBeVisible();
    await expect(filtersContent.getByLabel('API surface')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(moreFilters).toBeFocused();

    const groupBy = opened.usage.getByLabel('Group usage by');
    await groupBy.click();
    const groupByListbox = page.getByRole('listbox');
    await expect(groupByListbox).toBeVisible();
    await groupByListbox.getByRole('option', { name: 'Operation' }).click();
    await expect(groupBy).toContainText('Operation');
    const figure = opened.usage.getByTestId('usage-chart').getByRole('figure');
    expect(await figure.evaluate((node) => node.tagName)).toBe('FIGURE');
    await expect(figure).toHaveAccessibleName(/operation/i);
    await expect(figure).toHaveAccessibleDescription(/recorded floors.*≥.*—/i);

    const detailToggle = opened.usage
      .getByTestId('usage-recorded-table')
      .getByRole('row')
      .filter({ hasText: 'claude-sonnet-4' })
      .getByRole('button', { name: /details/i });
    await expect(detailToggle).toHaveAttribute('aria-expanded', 'false');
    await detailToggle.focus();
    await page.keyboard.press('Enter');
    await expect(detailToggle).toHaveAttribute('aria-expanded', 'true');
    const details = await controlledRegion(detailToggle, page);
    await expect(details).toBeVisible();
    await expect(details).toContainText('bedrock-primary');
    await expect(details).toContainText('bedrock-invoke-stream');
    await expect(details).toContainText('bedrock-converse-stream');
    await expect(detailToggle).toBeFocused();
    await page.keyboard.press('Space');
    await expect(detailToggle).toHaveAttribute('aria-expanded', 'false');
    expect(harness.unexpectedNonGetRequests).toEqual([]);
  });

  test('keeps duplicate backend buckets independently expandable', async ({ page }) => {
    const harness = await installProductionUsageApi(page, usagePayloadWithDuplicateBuckets);
    const opened = await openProductionUsage(page, '≥7,900');
    const table = opened.usage.getByTestId('usage-recorded-table');
    const duplicateRows = table.getByRole('row').filter({ hasText: 'gpt-5' });
    await expect(duplicateRows).toHaveCount(2);

    const toggles = duplicateRows.getByTestId('usage-row-toggle');
    const controlledIds: string[] = [];
    const toggleNames: Array<string | null> = [];
    for (let index = 0; index < 2; index += 1) {
      const toggle = toggles.nth(index);
      toggleNames.push(await toggle.getAttribute('aria-label'));
      await toggle.click();
      await expect(toggle).toHaveAttribute('aria-expanded', 'true');
      const details = await controlledRegion(toggle, page);
      await expect(details).toBeVisible();
      controlledIds.push((await toggle.getAttribute('aria-controls'))!);
    }

    expect(new Set(controlledIds).size).toBe(2);
    expect(new Set(toggleNames).size).toBe(2);
    expect(toggleNames.every((name) => /gpt-5.*Chat.*openai.*openai-primary/i.test(name || ''))).toBe(true);
    await expect(table.getByTestId('usage-row-details')).toHaveCount(2);
    expect(harness.unexpectedNonGetRequests).toEqual([]);
  });
});
