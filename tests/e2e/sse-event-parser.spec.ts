import { expect, test } from '@playwright/test';

test('run event parser prefers SSE header event over payload event wrapper', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const parser = await import('/providers/stream/runEventParser.ts' as string);

    const conflict = parser.parseRunSseEvent({
      event: 'text_delta',
      data: JSON.stringify({
        event: 'tool_result',
        data: { content: 'delta-from-wrapper' },
      }),
    });

    const fallback = parser.parseRunSseEvent({
      data: JSON.stringify({
        event: 'tool_result',
        data: { content: 'fallback-wrapper' },
      }),
    });

    const standard = parser.parseRunSseEvent({
      event: 'assistant_start',
      data: JSON.stringify({ ready: true }),
    });

    return { conflict, fallback, standard };
  });

  expect(result.conflict?.name).toBe('text_delta');
  expect(result.conflict?.payload).toEqual({ content: 'delta-from-wrapper' });

  expect(result.fallback?.name).toBe('tool_result');
  expect(result.fallback?.payload).toEqual({ content: 'fallback-wrapper' });

  expect(result.standard?.name).toBe('assistant_start');
  expect(result.standard?.payload).toEqual({ ready: true });
});

test('local context progress phases produce neutral status labels', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const chat = await import('/hooks/useChat.ts' as string);

    return [
      chat.deriveRunStatusFields('local_progress', {
        phase: 'context_compacting',
        message: 'CONTEXT_OVERFLOW recovery started',
        progress_percent: 25,
      }),
      chat.deriveRunStatusFields('local_progress', {
        phase: 'context_checkpoint_written',
        detail: 'checkpoint written after prompt context too large',
      }),
      chat.deriveRunStatusFields('local_progress', {
        phase: 'context_retrying_with_checkpoint',
        description: 'retrying after context window error',
      }),
      chat.deriveRunStatusFields('context_progress', {
        phase: 'tool_result_compacting',
        status: 'completed',
        detail: 'compacted large tool results',
        progress: { percent: 0.5 },
      }),
    ];
  });

  expect(result).toEqual([
    {
      phase: 'context_compacting',
      label: 'Compacting context',
      progressPercent: 25,
    },
    {
      phase: 'context_checkpoint_written',
      label: 'Context checkpoint saved',
    },
    {
      phase: 'context_retrying_with_checkpoint',
      label: 'Continuing with compacted context',
    },
    {
      phase: 'tool_result_compacting',
      label: 'Tool results compacted',
      progressPercent: 50,
    },
  ]);
});

test('long-running query lifecycle events explain waiting and resumption', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const chat = await import('/hooks/useChat.ts' as string);
    return [
      chat.deriveRunStatusFields('run_waiting_external', {
        dependency_ids: ['qj-bq-1'],
      }),
      chat.deriveRunStatusFields('run_resuming', {
        wake_kind: 'terminal',
      }),
      chat.deriveRunStatusFields('run_waiting_user', {
        dependency_ids: ['qj-bq-1'],
      }),
    ];
  });

  expect(result).toEqual([
    {
      phase: 'waiting_external',
      label: 'Waiting for database query',
      message: 'The query is still running. It will continue without occupying a model turn.',
    },
    {
      phase: 'resuming',
      label: 'Query update received',
      message: 'Resuming the task from its saved checkpoint.',
    },
    {
      phase: 'waiting_user',
      label: 'Task paused for review',
      message: 'Automatic continuations are paused. Required query work remains durable and was not abandoned or cancelled merely for taking time.',
    },
  ]);
});

test('diagnostic and terminal run events do not turn context recovery into assistant errors', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const chat = await import('/hooks/useChat.ts' as string);

    return {
      diagnosticStatus: chat.deriveRunStatusFields('run_diagnostic', {
        code: 'CONTEXT_FIXED_WINDOW_FALLBACK',
        message: 'context planner failed; applying fixed-window fallback',
        visibility: 'debug',
      }),
      suppressedErrorStatus: chat.deriveRunStatusFields('error', {
        message: 'context planner compact recovery failed: local model unavailable',
      }),
      terminalStatus: chat.deriveRunStatusFields('run_terminal', {
        status: 'failed',
        message: 'provider rejected the compacted prompt',
      }),
      errorClassification: [
        chat.isUserFacingRunErrorEvent('error', {
          message: 'context planner compact recovery failed: local model unavailable',
        }),
        chat.isUserFacingRunErrorEvent('run_diagnostic', {
          code: 'CONTEXT_PLANNER_FALLBACK',
          message: 'context planner fallback applied',
        }),
        chat.isUserFacingRunErrorEvent('run_terminal', {
          status: 'failed',
          message: 'terminal provider failure',
        }),
        chat.isUserFacingRunErrorEvent('error', {
          message: 'provider unavailable',
        }),
        chat.isUserFacingRunErrorEvent('error', {
          message: 'context recovery failed permanently',
          terminal: true,
        }),
      ],
    };
  });

  expect(result.diagnosticStatus).toEqual({
    phase: 'CONTEXT_FIXED_WINDOW_FALLBACK',
    label: 'Context adjusted',
  });
  expect(result.suppressedErrorStatus).toEqual({
    phase: 'context_progress',
    label: 'Context adjusted',
  });
  expect(result.terminalStatus).toEqual({
    phase: 'failed',
    label: 'Run failed',
    message: 'provider rejected the compacted prompt',
  });
  expect(result.errorClassification).toEqual([
    false,
    false,
    true,
    true,
    true,
  ]);
});

test('artifact reference context omits pinned artifact content', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const preview = await import('/lib/artifactPreview.ts' as string);
    const largeContent = 'SECRET_LARGE_CONTENT_'.repeat(500);
    const context = preview.buildArtifactReferenceContext([
      {
        id: 'artifact_123',
        type: 'data',
        title: 'Large customer export',
        content: largeContent,
        metadata: {
          tool_name: 'display_table',
          row_count: 100000,
          column_count: 2,
          columns: ['customer_id', 'lifetime_value'],
        },
      },
    ]);

    return {
      context,
      includesContent: context.includes('SECRET_LARGE_CONTENT_'),
    };
  });

  expect(result.includesContent).toBe(false);
  expect(result.context).toContain('Compact references only');
  expect(result.context).toContain('Artifact ID: artifact_123');
  expect(result.context).toContain('Shape: 100000 rows x 2 columns');
  expect(result.context).toContain('Content: not inlined');
});
