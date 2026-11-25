import { expect, test } from '@playwright/test';

test('query lifecycle never treats a successful launch as completed', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', {
    waitUntil: 'domcontentloaded',
  });

  const statuses = await page.evaluate(async () => {
    const lifecycle = await import('/lib/queryToolLifecycle.ts' as string);
    const envelope = (
      result: Record<string, unknown>,
      queryStatus?: string,
      ok = true,
    ) => ({
      artifact_id: 'query-artifact',
      tool_name: 'run_bigquery_query',
      ok,
      result_format: 'json' as const,
      result,
      query_metadata: {
        provider: 'bigquery' as const,
        duration_ms: 0,
        ...(queryStatus ? { status: queryStatus } : {}),
      },
    });

    return {
      runningWithNilJobId: lifecycle.resolveQueryToolLifecycleStatus({
        envelope: envelope({ status: 'running', job_id: 'nil' }),
      }),
      runningFromMetadataOnly: lifecycle.resolveQueryToolLifecycleStatus({
        envelope: envelope({ job_id: '00000-00000' }, 'running'),
      }),
      runningWithPartialRows: lifecycle.resolveQueryToolLifecycleStatus({
        envelope: envelope({ status: 'running', rows: [] }),
      }),
      acceptedWithoutTerminalProof: lifecycle.resolveQueryToolLifecycleStatus({
        envelope: envelope({ accepted: true }),
      }),
      completedExplicitly: lifecycle.resolveQueryToolLifecycleStatus({
        envelope: envelope({ status: 'completed' }),
      }),
      completedWithRows: lifecycle.resolveQueryToolLifecycleStatus({
        envelope: envelope({ rows: [] }),
      }),
      failedInvocation: lifecycle.resolveQueryToolLifecycleStatus({
        envelope: envelope({ error: 'provider rejected query' }, undefined, false),
      }),
      cancelled: lifecycle.resolveQueryToolLifecycleStatus({
        envelope: envelope({}, 'cancelled'),
      }),
    };
  });

  expect(statuses).toEqual({
    runningWithNilJobId: 'running',
    runningFromMetadataOnly: 'running',
    runningWithPartialRows: 'running',
    acceptedWithoutTerminalProof: 'unknown',
    completedExplicitly: 'completed',
    completedWithRows: 'completed',
    failedInvocation: 'failed',
    cancelled: 'cancelled',
  });
});

test('main-chat query badges follow the remote query lifecycle', async ({ page }) => {
  await page.goto('/dev/audit-trail', { waitUntil: 'domcontentloaded' });

  const running = page.locator('[data-query-tool-call-id="call_bq_running"]');
  await expect(running).toHaveAttribute('data-query-status', 'running');
  await expect(running.getByText('Running', { exact: true })).toBeVisible();
  await expect(running.getByText('Success', { exact: true })).toHaveCount(0);

  const completed = page.locator('[data-query-tool-call-id="call_bq_query"]');
  await expect(completed).toHaveAttribute('data-query-status', 'completed');
  await expect(completed.getByText('Success', { exact: true })).toBeVisible();

  const failed = page.locator('[data-query-tool-call-id="call_bq_error"]');
  await expect(failed).toHaveAttribute('data-query-status', 'failed');
  await expect(failed.getByText('Error', { exact: true })).toBeVisible();

  const cancelled = page.locator('[data-query-tool-call-id="call_dbx_cancelled"]');
  await expect(cancelled).toHaveAttribute('data-query-status', 'cancelled');
  await expect(cancelled.getByText('Cancelled', { exact: true })).toBeVisible();
});
