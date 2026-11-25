import { expect, test } from '@playwright/test';

// First-class local file results (LocalFilesRenderer). Contract under test:
// - file_search / local_list_folder resolve to the local renderer by exact
//   tool name (not the generic connector inspector);
// - provenance is explicit: location, read-only access, "Granted by you";
// - search hits are a real list, grouped per file, with expandable line
//   matches (aria-expanded contract) and honest truncation copy;
// - the preview affordance streams from the grant endpoint when available
//   and states plainly when the build lacks it — files are never copied.

const HARNESS = '/tests/e2e/fixtures/module-harness.html';

async function mount(page: import('@playwright/test').Page, preview: 'available' | 'unavailable') {
  await page.goto(HARNESS, { waitUntil: 'domcontentloaded' });
  await page.evaluate(async (mode) => {
    const harness = await import('/tests/e2e/fixtures/local-files-renderer-harness.tsx' as string);
    return harness.mountLocalFilesRendererHarness({ preview: mode });
  }, preview);
}

test('file_search renders as a local file result list with provenance and matches', async ({ page }) => {
  const browserErrors: string[] = [];
  page.on('pageerror', (error) => browserErrors.push(error.message));
  await mount(page, 'available');

  const search = page.getByTestId('local-files-case-search');
  await expect(search).toContainText('file_search');
  await expect(search).toContainText('Local Computer');
  await expect(search).toContainText('Read-only');
  await expect(search).toContainText('“forecast”');
  await expect(search).toContainText('~/Reports');
  await expect(search).toContainText('Granted by you');
  await expect(search).toContainText('3 matches across 2 files');
  // Honest truncation: shows the cap without pretending completeness.
  await expect(search).toContainText('Showing first 20');

  // Rows are a real list; content matches expand into line-level snippets
  // (open by default) behind an aria-expanded toggle.
  await expect(search.getByRole('listitem').filter({ hasText: 'notes/summary.md' })).toBeVisible();
  const linesToggle = search.getByRole('button', { name: 'Matching lines in notes/summary.md' });
  await expect(linesToggle).toHaveAttribute('aria-expanded', 'true');
  await expect(search).toContainText('forecast revised upward');
  await linesToggle.click();
  await expect(linesToggle).toHaveAttribute('aria-expanded', 'false');
  await expect(search).not.toContainText('forecast revised upward');

  // Name-only matches carry a name badge, no line rows.
  const nameHit = search.locator('text=Q3 forecast.xlsx').first();
  await expect(nameHit).toBeVisible();

  expect(browserErrors).toEqual([]);
});

test('local_list_folder renders folders and files with sizes', async ({ page }) => {
  await mount(page, 'available');

  const folder = page.getByTestId('local-files-case-folder');
  await expect(folder).toContainText('local_list_folder');
  await expect(folder).toContainText('1 folder · 2 files');
  await expect(folder).toContainText('archive/2025');
  await expect(folder).toContainText('archive/summary.csv');
  await expect(folder).toContainText('2 KB');
});

test('preview streams from the grant endpoint without copying the file', async ({ page }) => {
  await mount(page, 'available');

  const search = page.getByTestId('local-files-case-search');
  const previewToggle = search.getByRole('button', { name: 'Preview notes/summary.md' });
  await expect(previewToggle).toHaveAttribute('aria-expanded', 'false');
  await previewToggle.click();
  await expect(previewToggle).toHaveAttribute('aria-expanded', 'true');
  await expect(search).toContainText('forecast assumptions hold');
  // The streamed text is a labelled, keyboard-scrollable region.
  await expect(search.getByRole('region', { name: 'Preview of notes/summary.md' })).toBeVisible();
});

test('preview is honest when the stream endpoint is not in this build', async ({ page }) => {
  await mount(page, 'unavailable');

  const search = page.getByTestId('local-files-case-search');
  await search.getByRole('button', { name: 'Preview notes/summary.md' }).click();
  await expect(search).toContainText("Inline preview isn't available here yet.");
  await expect(search).toContainText('Ask the assistant to read this file');
});
