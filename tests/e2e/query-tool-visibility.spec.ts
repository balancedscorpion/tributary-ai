import { expect, test } from '@playwright/test';

test('query tool-call visibility hides internal polling calls', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const visibility = await import('/lib/queryToolVisibility.ts' as string);
    return {
      hidesCheckStatus: visibility.isHiddenQueryToolCallName('check_query_status'),
      hidesGetResults: visibility.isHiddenQueryToolCallName('get_query_results'),
      hidesArtifactSearch: visibility.isHiddenQueryToolCallName('search_artifact_content'),
      hidesJsonArtifact: visibility.isHiddenQueryToolCallName('get_json_artifact'),
      keepsRunQuery: visibility.isHiddenQueryToolCallName('run_bigquery_query'),
    };
  });

  expect(result.hidesCheckStatus).toBe(true);
  expect(result.hidesGetResults).toBe(true);
  expect(result.hidesArtifactSearch).toBe(true);
  expect(result.hidesJsonArtifact).toBe(true);
  expect(result.keepsRunQuery).toBe(false);
});

test('query tool-result suppression hides poll-only checks and keeps completed results', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const visibility = await import('/lib/queryToolVisibility.ts' as string);
    return {
      checkStatusSuppressed: visibility.shouldSuppressQueryToolResult({
        toolName: 'check_query_status',
        resultFormat: 'json',
        result: { status: 'running' },
      }),
      retryableSuppressed: visibility.shouldSuppressQueryToolResult({
        toolName: 'get_query_results',
        resultFormat: 'json',
        result: { status: 'running', retryable: true },
      }),
      nonTerminalSuppressed: visibility.shouldSuppressQueryToolResult({
        toolName: 'get_query_results',
        resultFormat: 'json',
        result: { status: 'running' },
      }),
      profilingSuppressed: visibility.shouldSuppressQueryToolResult({
        toolName: 'get_query_results',
        resultFormat: 'json',
        result: { status: 'profiling' },
      }),
      artifactSearchSuppressed: visibility.shouldSuppressQueryToolResult({
        toolName: 'search_artifact_content',
        resultFormat: 'json',
        result: { matches: [{ path: 'text_window.text' }] },
      }),
      completedVisible: visibility.shouldSuppressQueryToolResult({
        toolName: 'get_query_results',
        resultFormat: 'json',
        result: { status: 'completed', rows: [{ n: 1 }] },
      }),
    };
  });

  expect(result.checkStatusSuppressed).toBe(true);
  expect(result.retryableSuppressed).toBe(true);
  expect(result.nonTerminalSuppressed).toBe(true);
  expect(result.profilingSuppressed).toBe(true);
  expect(result.artifactSearchSuppressed).toBe(true);
  expect(result.completedVisible).toBe(false);
});
