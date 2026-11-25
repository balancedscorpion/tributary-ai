import { expect, test } from '@playwright/test';

test('managed query ids reject legacy sentinels across polling derivations', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const ids = await import('/lib/managedQueryJobId.ts' as string);
    const shared = await import('/hooks/useSharedQueryStatus.ts' as string);
    const targets = await import('/lib/asyncQueryPollTargets.ts' as string);
    const artifacts = await import('/lib/toolResultArtifact.ts' as string);

    const invalid = [
      '',
      'nil',
      'null',
      '<nil>',
      '00000-00000',
      '00000000-0000-0000-0000-000000000000',
      'qj-00000-00000',
      'qj-nil',
      'provider-statement-id',
      'qj-_malformed',
      'qj-bad/value',
    ];
    const valid = ['qj-dbx-0123456789abcdef', ' qj-attempt_2 '];
    const pollTargets = targets.collectAsyncQueryPollTargets({
      invalidNil: {
        id: 'invalidNil',
        type: 'data',
        title: 'Invalid nil',
        content: '',
        metadata: { async_query: true, placeholder: true, job_id: 'nil', thread_id: 'thread-1' },
      },
      invalidZero: {
        id: 'invalidZero',
        type: 'data',
        title: 'Invalid zero',
        content: '',
        metadata: { async_query: true, placeholder: true, job_id: '00000-00000', thread_id: 'thread-1' },
      },
      valid: {
        id: 'valid',
        type: 'data',
        title: 'Valid',
        content: '',
        metadata: {
          async_query: true,
          placeholder: true,
          job_id: 'qj-dbx-0123456789abcdef',
          thread_id: 'thread-1',
        },
      },
    });
    const invalidArtifact = artifacts.buildArtifactFromEnvelope({
      artifact_id: 'invalid-artifact',
      tool_name: 'run_databricks_query',
      ok: true,
      result_format: 'json',
      result: { job_id: '00000-00000', provider: 'databricks', status: 'running' },
    }, 'run_databricks_query', null);

    return {
      invalid: invalid.map((value) => ids.normalizeManagedQueryJobId(value)),
      valid: valid.map((value) => ids.normalizeManagedQueryJobId(value)),
      invalidKeys: invalid.map((value) => shared.getSharedQueryStatusKey(value, 'thread-1')),
      pollTargetIds: pollTargets.map((target: { jobId: string }) => target.jobId),
      invalidArtifactIsPlaceholder: invalidArtifact.metadata?.placeholder === true,
      invalidArtifactJobId: invalidArtifact.metadata?.job_id,
    };
  });

  expect(result.invalid).toEqual(Array(11).fill(undefined));
  expect(result.valid).toEqual(['qj-dbx-0123456789abcdef', 'qj-attempt_2']);
  expect(result.invalidKeys).toEqual(Array(11).fill(null));
  expect(result.pollTargetIds).toEqual(['qj-dbx-0123456789abcdef']);
  expect(result.invalidArtifactIsPlaceholder).toBe(false);
  expect(result.invalidArtifactJobId).toBeUndefined();
});
