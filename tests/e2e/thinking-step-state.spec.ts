import { expect, test } from '@playwright/test';

test('thinking-step activity is exclusive to the newest visible assistant step', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const thinkingStepState = await import('/lib/thinkingStepState.ts' as string);

    return {
      priorVisibleStepStreaming: thinkingStepState.deriveThinkingStepIsActive({
        isStreaming: true,
        isLatestAssistant: true,
        isLatestVisibleStep: false,
        toolCalls: [],
        messageIdentityKey: 'msg-1',
      }),
      latestVisibleStepStreaming: thinkingStepState.deriveThinkingStepIsActive({
        isStreaming: true,
        isLatestAssistant: true,
        isLatestVisibleStep: true,
        toolCalls: [],
        messageIdentityKey: 'msg-1',
      }),
      latestVisibleStepButNotLatestAssistant: thinkingStepState.deriveThinkingStepIsActive({
        isStreaming: true,
        isLatestAssistant: false,
        isLatestVisibleStep: true,
        toolCalls: [],
        messageIdentityKey: 'msg-1',
      }),
    };
  });

  expect(result.priorVisibleStepStreaming).toBe(false);
  expect(result.latestVisibleStepStreaming).toBe(true);
  expect(result.latestVisibleStepButNotLatestAssistant).toBe(false);
});

test('thinking-step activity tracks unresolved tool execution for latest visible step only', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const thinkingStepState = await import('/lib/thinkingStepState.ts' as string);
    const queryCardStateKey = await import('/lib/queryCardStateKey.ts' as string);

    const nonSqlToolCall = {
      id: 'file-1',
      name: 'read_file',
      args: { path: 'a.sql' },
      _uiStartedAt: Date.now(),
    };

    const sqlToolCall = {
      id: 'sql-1',
      name: 'run_bigquery_query',
      args: { sql: 'select 1' },
      _uiStartedAt: Date.now(),
      _uiRunId: 'run-1',
    };

    const sqlScopedKey = queryCardStateKey.getQueryCardStateKey({
      runId: sqlToolCall._uiRunId,
      messageIdentityKey: 'msg-1',
      toolCallId: sqlToolCall.id,
    });

    const unresolvedNonSql = thinkingStepState.deriveThinkingStepIsActive({
      isStreaming: false,
      isLatestAssistant: true,
      isLatestVisibleStep: true,
      toolCalls: [nonSqlToolCall],
      messageIdentityKey: 'msg-1',
      toolResultsByCallId: new Map(),
    });

    const resolvedNonSql = thinkingStepState.deriveThinkingStepIsActive({
      isStreaming: false,
      isLatestAssistant: true,
      isLatestVisibleStep: true,
      toolCalls: [nonSqlToolCall],
      messageIdentityKey: 'msg-1',
      toolResultsByCallId: new Map([['file-1', { rawContent: '{}' }]]),
    });

    const unresolvedSql = thinkingStepState.deriveThinkingStepIsActive({
      isStreaming: false,
      isLatestAssistant: true,
      isLatestVisibleStep: true,
      toolCalls: [sqlToolCall],
      messageIdentityKey: 'msg-1',
      sqlToolResultsByScopedKey: new Map(),
    });

    const resolvedSql = thinkingStepState.deriveThinkingStepIsActive({
      isStreaming: false,
      isLatestAssistant: true,
      isLatestVisibleStep: true,
      toolCalls: [sqlToolCall],
      messageIdentityKey: 'msg-1',
      sqlToolResultsByScopedKey: new Map(
        sqlScopedKey ? [[sqlScopedKey, { envelope: { tool_call_id: 'sql-1' } }]] : [],
      ),
    });

    const unresolvedNonSqlButPriorVisible = thinkingStepState.deriveThinkingStepIsActive({
      isStreaming: false,
      isLatestAssistant: true,
      isLatestVisibleStep: false,
      toolCalls: [nonSqlToolCall],
      messageIdentityKey: 'msg-1',
      toolResultsByCallId: new Map(),
    });

    return {
      unresolvedNonSql,
      resolvedNonSql,
      unresolvedSql,
      resolvedSql,
      unresolvedNonSqlButPriorVisible,
    };
  });

  expect(result.unresolvedNonSql).toBe(true);
  expect(result.resolvedNonSql).toBe(false);
  expect(result.unresolvedSql).toBe(true);
  expect(result.resolvedSql).toBe(false);
  expect(result.unresolvedNonSqlButPriorVisible).toBe(false);
});
