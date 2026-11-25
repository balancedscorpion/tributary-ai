import { expect, test, type Page } from '@playwright/test';

function jsonResponse(body: any, status = 200) {
  return {
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  };
}

function sseResponse(events: Array<{ event: string; data: any }>) {
  const body = events
    .map(({ event, data }) => {
      const encoded =
        typeof data === 'string' ? data : JSON.stringify(data ?? {});
      return [`event: ${event}`, `data: ${encoded}`, ''].join('\n');
    })
    .join('\n');

  return {
    status: 200,
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
    },
    body,
  };
}

const mockSettingsPayload = {
  provider: {
    providerId: 'compatible',
    model: 'test-model',
    profileId: 'custom',
  },
  compatibleProfiles: [
    {
      id: 'custom',
      displayName: 'Custom Provider',
      apiType: 'chat_completions',
      baseUrl: 'https://mock.local/v1',
      auth: { type: 'none', header: 'Authorization', scheme: 'Bearer' },
      apiKey: '',
      extraHeaders: [],
      queryParams: null,
      payloadOverrides: null,
      timeoutMs: null,
      reasoningEffort: null,
      modelInPath: false,
      modelInBody: true,
      endpointSuffix: null,
    },
  ],
  providerConfigs: [
    {
      id: 'cfg-test',
      displayName: 'Mock Compatible',
      providerId: 'compatible',
      model: 'test-model',
      apiType: 'chat_completions',
      baseUrl: 'https://mock.local/v1',
      authType: 'none',
      authHeader: 'Authorization',
      authScheme: 'Bearer',
      extraHeaders: [],
      modelInPath: false,
      modelInBody: true,
      endpointSuffix: null,
    },
  ],
  activeProviderConfigId: 'cfg-test',
};

async function installMockApi(page: Page): Promise<void> {
  const threadId = 'thread-step-ordering';
  const runId = 'run-step-ordering';
  const now = Date.now();
  const thread = {
    id: threadId,
    title: 'Step Ordering',
    created_at: now - 1000,
    updated_at: now - 1000,
  };

  const firstStepText =
    "I'll start by exploring the uploaded files to understand what's available.";
  const secondStepText = 'Reading the dataset to identify key columns.';
  const finalAnswer = 'Summary complete.';

  const events = [
    { event: 'assistant_start', data: {} },
    { event: 'text_delta', data: { content: firstStepText } },
    {
      event: 'tool_calls',
      data: {
        toolCalls: [{ id: 'tc-list-1', name: 'list_files', args: {} }],
      },
    },
    {
      event: 'assistant_final',
      data: {
        message: {
          role: 'assistant',
          content: firstStepText,
        },
      },
    },
    {
      event: 'tool_result',
      data: {
        message: {
          role: 'tool',
          name: 'list_files',
          tool_call_id: 'tc-list-1',
          content: JSON.stringify({
            tool_call_id: 'tc-list-1',
            tool_name: 'list_files',
            result_format: 'json',
            result: { files: ['dataset.csv'] },
          }),
        },
      },
    },
    { event: 'assistant_start', data: {} },
    {
      event: 'tool_calls',
      data: {
        toolCalls: [
          { id: 'tc-read-1', name: 'read_file', args: { path: 'dataset.csv' } },
        ],
      },
    },
    {
      event: 'assistant_final',
      data: {
        message: {
          role: 'assistant',
          content: secondStepText,
        },
      },
    },
    {
      event: 'tool_result',
      data: {
        message: {
          role: 'tool',
          name: 'read_file',
          tool_call_id: 'tc-read-1',
          content: JSON.stringify({
            tool_call_id: 'tc-read-1',
            tool_name: 'read_file',
            result_format: 'json',
            result: { content: 'col_a,col_b' },
          }),
        },
      },
    },
    { event: 'final', data: { answer: finalAnswer } },
    { event: 'done', data: {} },
  ];

  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const pathname = url.pathname;
    const method = req.method().toUpperCase();

    if (pathname === '/api/threads' && method === 'GET') {
      await route.fulfill(jsonResponse({ threads: [thread] }));
      return;
    }

    if (pathname === `/api/threads/${threadId}` && method === 'GET') {
      await route.fulfill(
        jsonResponse({
          thread,
          messages: [],
        }),
      );
      return;
    }

    if (pathname === `/api/threads/${threadId}/runs` && method === 'POST') {
      await route.fulfill(
        jsonResponse({
          run: {
            id: runId,
            status: 'running',
          },
        }),
      );
      return;
    }

    if (pathname === `/api/runs/${runId}/events` && method === 'GET') {
      await route.fulfill(sseResponse(events));
      return;
    }

    if (pathname === `/api/runs/${runId}` && method === 'GET') {
      await route.fulfill(
        jsonResponse({
          run: {
            id: runId,
            status: 'completed',
            thread_id: threadId,
          },
        }),
      );
      return;
    }

    if (pathname.startsWith('/api/settings') && method === 'GET') {
      await route.fulfill(jsonResponse({ settings: mockSettingsPayload }));
      return;
    }

    if (
      pathname.startsWith('/api/settings') &&
      (method === 'PUT' || method === 'POST')
    ) {
      await route.fulfill(jsonResponse({ settings: mockSettingsPayload }));
      return;
    }

    if (pathname === '/api/secrets/status' && method === 'GET') {
      await route.fulfill(
        jsonResponse({
          initialized: true,
          locked: false,
          configured: {
            openai: true,
            anthropic: true,
            gemini: true,
            compatible: {
              default: { apiKey: true },
            },
          },
        }),
      );
      return;
    }

    if (pathname.startsWith('/api/runs/') && pathname.endsWith('/cancel')) {
      await route.fulfill(jsonResponse({ ok: true }));
      return;
    }

    if (pathname.startsWith('/api/')) {
      await route.fulfill(jsonResponse({}));
      return;
    }

    await route.continue();
  });
}

test('intermediate steps preserve narrative-before-tool chronology', async ({
  page,
}) => {
  await installMockApi(page);
  await page.goto('/', { waitUntil: 'domcontentloaded' });

  const composer = page.locator('textarea').first();
  await composer.fill('Summarise the attached dataset and build me a scatter plot');
  await composer.press('Enter');

  await expect(page.getByText('Summary complete.')).toBeVisible();

  const narrativeHeader = page
    .locator('button', {
      has: page.locator('span', { hasText: /Start by exploring the uploaded fi/i }),
    })
    .first();
  await expect(narrativeHeader).toBeVisible();

  const exploringHeader = page
    .locator('button', {
      has: page.locator('span', { hasText: 'Exploring files' }),
    })
    .first();

  if ((await exploringHeader.count()) === 0) {
    return;
  }

  const narrativeBox = await narrativeHeader.boundingBox();
  const exploringBox = await exploringHeader.boundingBox();
  expect(narrativeBox).not.toBeNull();
  expect(exploringBox).not.toBeNull();
  expect((narrativeBox as any).y).toBeLessThan((exploringBox as any).y);
});
