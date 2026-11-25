import { expect, test } from '@playwright/test';

test('query explorer hydrates preview rows and pages with server next offsets', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const mod = await import('/tests/e2e/fixtures/query-explorer-harness.tsx' as string);
    return mod.runQueryExplorerLivePagingScenario();
  });

  expect(result.snapshots.some((snapshot: any) => snapshot.rowsLength === 10)).toBe(true);
  expect(result.snapshots.some((snapshot: any) => snapshot.rowsLength === 50)).toBe(true);
  expect(result.snapshots[result.snapshots.length - 1]).toMatchObject({
    rowsLength: 100,
    totalRows: 100,
    hasMore: false,
    loading: false,
    error: null,
  });

  const offsets = result.calls.map((raw: string) => new URL(raw, 'http://127.0.0.1').searchParams.get('offset'));
  expect(offsets).toEqual(['0', '50']);
});
