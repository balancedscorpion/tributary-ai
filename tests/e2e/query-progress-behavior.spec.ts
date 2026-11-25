import { expect, test } from '@playwright/test';

/**
 * Runtime behavior tests for the shared query progress policy
 * (lib/queryProgress.ts), exercised through the vite module harness so the
 * real TypeScript implementation runs — these are not source-string checks.
 *
 * Contract under test:
 *  - active statuses are indeterminate until the provider reports defensible
 *    progress, then never move backwards or reach 100 early;
 *  - completed snaps to exactly 100;
 *  - failed/cancelled settle to null (the bar hides/settles, no stale %);
 *  - the polling snapshot merge keeps progress monotonic across provider
 *    dips and stops at a terminal status.
 */

test('display progress is monotonic while active and exact at terminal states', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const mod = await import('/lib/queryProgress.ts' as string);
    const artifacts = await import('/lib/toolResultArtifact.ts' as string);

    // Simulated inspector ticks: indeterminate, then provider reports,
    // a provider dip, a higher report, then completion.
    const ticks: Array<Record<string, unknown>> = [
      { status: 'running', elapsedMs: 5_000 },
      { status: 'running', elapsedMs: 20_000, progressPercent: 40 },
      { status: 'running', elapsedMs: 30_000, progressPercent: 12 },
      { status: 'running', elapsedMs: 40_000, progressPercent: 55 },
      { status: 'completed', elapsedMs: 41_000 },
    ];
    const sequence: Array<number | null> = [];
    let previous: number | null = null;
    for (const tick of ticks) {
      const next = mod.resolveQueryDisplayProgressPercent({
        ...tick,
        previousProgressPercent: previous,
      });
      sequence.push(next);
      if (typeof next === 'number') previous = next;
    }

    const failed = mod.resolveQueryDisplayProgressPercent({
      status: 'failed',
      elapsedMs: 10_000,
      previousProgressPercent: 80,
    });
    const cancelled = mod.resolveQueryDisplayProgressPercent({
      status: 'cancelled',
      elapsedMs: 10_000,
      previousProgressPercent: 80,
    });

    const telemetryWithoutDenominator = mod.getDeterministicQueryToolProgressPercent({
      provider: 'bigquery',
      phase: 'polling',
      bytes_processed: 1024 ** 4,
      timestamp: Date.now(),
    });

    // Polling snapshot merges: provider dip must not lower progress, and a
    // terminal merge must land on exactly 100.
    const startedAt = 1_000;
    let snapshot: Record<string, unknown> = {};
    const merged: Array<number | null> = [];
    const payloads = [
      { status: 'running', progress_percent: 40, elapsed_ms: 10_000 },
      { status: 'running', progress_percent: 25, elapsed_ms: 12_000 },
      { status: 'running', progress_percent: 70, elapsed_ms: 14_000 },
      { status: 'completed', elapsed_ms: 15_000 },
    ];
    for (const payload of payloads) {
      snapshot = mod.mergeQueryStatusSnapshot(
        snapshot,
        payload,
        startedAt,
        startedAt + Number(payload.elapsed_ms ?? 0),
      );
      merged.push(typeof snapshot.progress_percent === 'number' ? snapshot.progress_percent : null);
    }

    const profilingArtifact = artifacts.buildArtifactFromEnvelope({
      artifact_id: 'art-profile',
      tool_name: 'run_bigquery_query',
      ok: true,
      result_format: 'json',
      result: {
        job_id: 'qj-profile',
        owner_token: 'qown-profile',
        provider: 'bigquery',
        status: 'profiling',
        profile_outcome: '',
        remote_submitted: false,
      },
      query_metadata: {
        provider: 'bigquery',
        duration_ms: 0,
        job_id: 'qj-profile',
        status: 'profiling',
        remote_submitted: false,
        profile_phase: 'dry_run',
      },
    }, 'run_bigquery_query', null);

    return {
      sequence,
      failed,
      cancelled,
      telemetryWithoutDenominator,
      merged,
      finalStatus: snapshot.status,
      terminalCheck: mod.isTerminalQueryStatus(snapshot.status),
      durableActiveStates: [
        'reserved',
        'profiling',
        'submission_unknown',
        'submitted',
        'running',
        'cancel_requested',
        'cancelling',
      ].map((status) => mod.isActiveQueryStatus(status)),
      durableTerminalStates: ['completed', 'failed', 'cancelled', 'expired', 'orphaned']
        .map((status) => mod.isTerminalQueryStatus(status)),
      profilingArtifact: {
        title: profilingArtifact.title,
        status: profilingArtifact.metadata?.status,
        queryStatus: (profilingArtifact.metadata?.query_metadata as Record<string, unknown> | undefined)?.status,
        placeholder: profilingArtifact.metadata?.placeholder,
      },
    };
  });

  // Elapsed time alone is indeterminate. Provider values are monotonic and
  // capped below 100 while execution remains active.
  expect(result.sequence[0]).toBeNull();
  const active = result.sequence.slice(1, 4) as number[];
  for (const value of active) {
    expect(typeof value).toBe('number');
    expect(value).toBeGreaterThan(0);
    expect(value).toBeLessThan(100);
  }
  for (let i = 1; i < active.length; i++) {
    expect(active[i]).toBeGreaterThanOrEqual(active[i - 1]);
  }
  // The dip tick (server reported 12 while display was higher) must hold.
  expect(active[1]).toBeGreaterThanOrEqual(active[0]);

  // Completion snaps to exactly 100; failure/cancel settle to null.
  expect(result.sequence[4]).toBe(100);
  expect(result.failed).toBeNull();
  expect(result.cancelled).toBeNull();

  // Bytes/rows/elapsed telemetry without a provider denominator must not be
  // converted into a made-up percentage.
  expect(result.telemetryWithoutDenominator).toBeNull();

  // Snapshot merges: dip held, then exact 100 at completion.
  expect(result.merged[0]).toBe(40);
  expect(result.merged[1]).toBeGreaterThanOrEqual(40);
  expect(result.merged[2]).toBeGreaterThanOrEqual(result.merged[1] as number);
  expect(result.merged[3]).toBe(100);
  expect(result.finalStatus).toBe('completed');
  expect(result.terminalCheck).toBe(true);
  expect(result.durableActiveStates).toEqual([true, true, true, true, true, true, true]);
  expect(result.durableTerminalStates).toEqual([true, true, true, true, true]);
  expect(result.profilingArtifact).toEqual({
    title: 'Query Profiling',
    status: 'profiling',
    queryStatus: 'profiling',
    placeholder: true,
  });
});
