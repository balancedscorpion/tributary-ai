import { expect, test } from '@playwright/test';

test('intermediate classifier skips empty poll-only assistant turns', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const thinking = await import('/lib/intermediateThinking.ts' as string);

    const pollOnlyThenFinal = [
      {
        role: 'assistant',
        content: '',
        tool_calls: [{ id: 'tc-1', name: 'get_query_results', args: { job_id: 'job-1' } }],
      },
      {
        role: 'assistant',
        content: 'Query complete.',
      },
    ];

    const visibleToolThenFinal = [
      {
        role: 'assistant',
        content: '',
        tool_calls: [{ id: 'tc-2', name: 'run_bigquery_query', args: { sql: 'select 1' } }],
      },
      {
        role: 'assistant',
        content: 'Done.',
      },
    ];

    const narrativeThenFinal = [
      {
        role: 'assistant',
        content: 'I will check the data source first.',
      },
      {
        role: 'assistant',
        content: 'All set.',
      },
    ];

    const toolAndNarrativeSameTurn = [
      {
        role: 'assistant',
        content: 'Done. I found the answer.',
        tool_calls: [{ id: 'tc-4', name: 'run_bigquery_query', args: { sql: 'select 1' } }],
      },
    ];

    const toolAndNarrativeThenFinal = [
      {
        role: 'assistant',
        content: 'Running query now.',
        tool_calls: [{ id: 'tc-5', name: 'run_bigquery_query', args: { sql: 'select 1' } }],
      },
      {
        role: 'assistant',
        content: 'Here is the result.',
      },
    ];

    return {
      pollOnlyFlags: thinking.deriveIntermediateFlags(pollOnlyThenFinal as any),
      visibleToolFlags: thinking.deriveIntermediateFlags(visibleToolThenFinal as any),
      narrativeFlags: thinking.deriveIntermediateFlags(narrativeThenFinal as any),
      toolNarrativeSingleFlags: thinking.deriveIntermediateFlags(toolAndNarrativeSameTurn as any),
      toolNarrativeThenFinalFlags: thinking.deriveIntermediateFlags(toolAndNarrativeThenFinal as any),
    };
  });

  expect(result.pollOnlyFlags).toEqual([false, false]);
  expect(result.visibleToolFlags).toEqual([true, false]);
  expect(result.narrativeFlags).toEqual([true, false]);
  expect(result.toolNarrativeSingleFlags).toEqual([false]);
  expect(result.toolNarrativeThenFinalFlags).toEqual([true, false]);
});

test('thinking-step visibility hides polling-only tool steps', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const thinking = await import('/lib/intermediateThinking.ts' as string);

    return {
      textOnly: thinking.isVisibleThinkingStepMessage({ tool_calls: [] } as any),
      pollOnly: thinking.isVisibleThinkingStepMessage({
        tool_calls: [{ id: 'tc-1', name: 'get_query_results', args: {} }],
      } as any),
      mixedTools: thinking.isVisibleThinkingStepMessage({
        tool_calls: [
          { id: 'tc-2', name: 'get_query_results', args: {} },
          { id: 'tc-3', name: 'run_bigquery_query', args: { sql: 'select 1' } },
        ],
      } as any),
    };
  });

  expect(result.textOnly).toBe(true);
  expect(result.pollOnly).toBe(false);
  expect(result.mixedTools).toBe(true);
});
