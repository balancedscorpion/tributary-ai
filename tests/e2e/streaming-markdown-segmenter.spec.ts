import { expect, test } from '@playwright/test';

test('streaming markdown segmenter handles split fences and stable-prefix snapshots', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const snapshots = await page.evaluate(async () => {
    const mod = await import('/lib/streamingMarkdownSegmenter.ts' as string);
    const segmenter = mod.createStreamingMarkdownSegmenter({ tailReparseBytes: 12 });

    const s1 = segmenter.push('Intro line\n`');
    const s2 = segmenter.push('``sql\nSELECT 1\n');
    const s3 = segmenter.push('```\nDone\n');

    return [s1, s2, s3];
  });

  expect(snapshots[0].fullText).toBe('Intro line\n`');
  expect(snapshots[0].inCodeBlock).toBe(false);
  expect(snapshots[1].fullText).toBe('Intro line\n```sql\nSELECT 1\n');
  expect(snapshots[1].inCodeBlock).toBe(true);
  expect(snapshots[2].fullText).toBe('Intro line\n```sql\nSELECT 1\n```\nDone\n');
  expect(snapshots[2].inCodeBlock).toBe(false);

  // Stable prefix should advance over time while preserving whole-line boundaries.
  expect(snapshots[2].stablePrefix.length).toBeGreaterThan(0);
  expect(snapshots[2].fullText).toBe(`${snapshots[2].stablePrefix}${snapshots[2].unstableSuffix}`);
  expect(snapshots[2].stablePrefix.endsWith('\n')).toBe(true);
  expect(snapshots[2].unstableSuffix).toBe('SELECT 1\n```\nDone\n');
});
