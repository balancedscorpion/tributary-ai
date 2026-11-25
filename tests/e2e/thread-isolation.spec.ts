import { expect, test, type Page } from '@playwright/test';

type MockThread = {
  id: string;
  title: string;
  created_at: number;
  updated_at: number;
};

type RunPost = {
  threadId: string;
  path: string;
  body: any;
};

type MockApiOptions = {
  delayRunCreateMsByThread?: Record<string, number>;
  recycledThreadIds?: string[];
};

type MockApiState = {
  runPosts: RunPost[];
  requests: string[];
};

function jsonResponse(body: any, status = 200) {
  return {
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
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

function sseResponse(answer: string) {
  const body = [
    'event: assistant_start',
    'data: {}',
    '',
    'event: final',
    `data: ${JSON.stringify({ answer })}`,
    '',
    'event: done',
    'data: {}',
    '',
  ].join('\n');
  return {
    status: 200,
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
    },
    body,
  };
}

async function installMockApi(page: Page, options?: MockApiOptions): Promise<MockApiState> {
  const runPosts: RunPost[] = [];
  const requests: string[] = [];
  const delayRunCreateMsByThread = options?.delayRunCreateMsByThread ?? {};
  const recycledThreadIds = [...(options?.recycledThreadIds ?? [])];

  const now = Date.now();
  const threads: MockThread[] = [
    { id: 'thread-a', title: 'Thread A', created_at: now - 8_000, updated_at: now - 4_000 },
    { id: 'thread-b', title: 'Thread B', created_at: now - 7_000, updated_at: now - 3_000 },
  ];
  const messagesByThread = new Map<string, any[]>([
    ['thread-a', []],
    ['thread-b', []],
  ]);
  const runsById = new Map<string, { threadId: string; answer: string }>();
  let runCounter = 0;
  let threadCounter = 2;

  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const pathname = url.pathname;
    const method = req.method().toUpperCase();
    requests.push(`${method} ${pathname}`);

    const threadRunMatch = pathname.match(/^\/api\/threads\/([^/]+)\/runs$/);
    if (threadRunMatch && method === 'POST') {
      const threadId = decodeURIComponent(threadRunMatch[1] || '');
      const delayMs = Math.max(0, Number(delayRunCreateMsByThread[threadId] ?? 0));
      if (delayMs > 0) {
        await new Promise((resolve) => setTimeout(resolve, delayMs));
      }

      const bodyText = req.postData() || '{}';
      let parsedBody: any = {};
      try {
        parsedBody = JSON.parse(bodyText);
      } catch {
        parsedBody = {};
      }
      runPosts.push({ threadId, path: pathname, body: parsedBody });

      const runId = `run-${++runCounter}`;
      runsById.set(runId, { threadId, answer: `ok-${threadId}` });

      const threadMessages = messagesByThread.get(threadId) || [];
      const userContent = String(parsedBody?.message?.content ?? parsedBody?.content ?? '').trim();
      if (userContent) {
        threadMessages.push({
          id: `${threadId}-msg-${threadMessages.length + 1}`,
          seq: threadMessages.length + 1,
          role: 'user',
          content: userContent,
          referenced_artifact_ids: parsedBody?.message?.referenced_artifact_ids ?? parsedBody?.message?.referencedArtifactIds ?? [],
          referenced_message_ids: parsedBody?.message?.referenced_message_ids ?? parsedBody?.message?.referencedMessageIds ?? [],
        });
        messagesByThread.set(threadId, threadMessages);
      }

      await route.fulfill(jsonResponse({
        run: {
          id: runId,
          status: 'running',
        },
      }));
      return;
    }

    const runEventsMatch = pathname.match(/^\/api\/runs\/([^/]+)\/events$/);
    if (runEventsMatch && method === 'GET') {
      const runId = decodeURIComponent(runEventsMatch[1] || '');
      const run = runsById.get(runId);
      await route.fulfill(sseResponse(run?.answer || 'ok'));
      return;
    }

    const runStatusMatch = pathname.match(/^\/api\/runs\/([^/]+)$/);
    if (runStatusMatch && method === 'GET') {
      const runId = decodeURIComponent(runStatusMatch[1] || '');
      const run = runsById.get(runId);
      await route.fulfill(jsonResponse({
        run: {
          id: runId,
          status: run ? 'running' : 'completed',
          thread_id: run?.threadId,
        },
      }));
      return;
    }

    if (pathname === '/api/threads' && method === 'GET') {
      await route.fulfill(jsonResponse({ threads }));
      return;
    }

    if (pathname === '/api/threads' && method === 'POST') {
      const recycled = recycledThreadIds.shift();
      const nextID = (recycled && !threads.some((t) => t.id === recycled))
        ? recycled
        : `thread-${++threadCounter}`;
      const next: MockThread = {
        id: nextID,
        title: nextID === 'thread-b' ? 'Thread B' : `Thread ${threadCounter}`,
        created_at: Date.now(),
        updated_at: Date.now(),
      };
      threads.unshift(next);
      messagesByThread.set(next.id, []);
      await route.fulfill(jsonResponse({ thread: next }));
      return;
    }

    const threadDetailMatch = pathname.match(/^\/api\/threads\/([^/]+)$/);
    if (threadDetailMatch && method === 'GET') {
      const threadId = decodeURIComponent(threadDetailMatch[1] || '');
      const thread = threads.find((t) => t.id === threadId);
      if (!thread) {
        await route.fulfill(jsonResponse({ error: 'not found' }, 404));
        return;
      }
      await route.fulfill(jsonResponse({
        thread,
        messages: messagesByThread.get(threadId) || [],
      }));
      return;
    }

    if (threadDetailMatch && method === 'DELETE') {
      const threadId = decodeURIComponent(threadDetailMatch[1] || '');
      const idx = threads.findIndex((t) => t.id === threadId);
      if (idx >= 0) {
        threads.splice(idx, 1);
      }
      messagesByThread.delete(threadId);
      await route.fulfill(jsonResponse({ ok: true }));
      return;
    }

    if (pathname.startsWith('/api/settings') && method === 'GET') {
      await route.fulfill(jsonResponse({ settings: mockSettingsPayload }));
      return;
    }

    if (pathname.startsWith('/api/settings') && (method === 'PUT' || method === 'POST')) {
      await route.fulfill(jsonResponse({ settings: mockSettingsPayload }));
      return;
    }

    if (pathname === '/api/secrets/status' && method === 'GET') {
      await route.fulfill(jsonResponse({
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
      }));
      return;
    }

    if (pathname.startsWith('/api/runs/') && pathname.endsWith('/cancel') && method === 'POST') {
      await route.fulfill(jsonResponse({ ok: true }));
      return;
    }

    if (pathname.startsWith('/api/')) {
      await route.fulfill(jsonResponse({}));
      return;
    }

    await route.continue();
  });

  return { runPosts, requests };
}

function threadRow(page: Page, title: string) {
  return page.locator('div[role="button"]', { hasText: title }).first();
}

test('send stays pinned to invocation thread while switching active thread', async ({ page }) => {
  const mock = await installMockApi(page, {
    delayRunCreateMsByThread: {
      'thread-a': 300,
    },
  });

  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect.poll(() => mock.requests.some((line) => line === 'GET /api/settings')).toBeTruthy();
  await expect(threadRow(page, 'Thread A')).toBeVisible();
  await expect(threadRow(page, 'Thread B')).toBeVisible();

  const composer = page.locator('textarea').first();
  await threadRow(page, 'Thread A').click();
  await composer.fill('Message for thread A');
  await composer.press('Enter');

  await threadRow(page, 'Thread B').click();

  await expect.poll(() => mock.runPosts.length).toBe(1);
  expect(mock.runPosts[0]?.threadId).toBe('thread-a');
  expect(mock.runPosts[0]?.path).toBe('/api/threads/thread-a/runs');
});

test('composer draft state is thread-local and delete lifecycle clears stale drafts', async ({ page }) => {
  const mock = await installMockApi(page, {
    recycledThreadIds: ['thread-b'],
  });

  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect.poll(() => mock.requests.some((line) => line === 'GET /api/settings')).toBeTruthy();
  await expect(threadRow(page, 'Thread A')).toBeVisible();
  await expect(threadRow(page, 'Thread B')).toBeVisible();

  const composer = page.locator('textarea').first();

  await threadRow(page, 'Thread A').click();
  await composer.fill('Draft A');

  await threadRow(page, 'Thread B').click();
  await composer.fill('Draft B');

  await threadRow(page, 'Thread A').click();
  await expect(composer).toHaveValue('Draft A');

  await composer.press('Enter');
  await expect.poll(() => mock.runPosts.filter((run) => run.threadId === 'thread-a').length).toBe(1);
  await expect(composer).toHaveValue('');

  await threadRow(page, 'Thread B').click();
  await expect(composer).toHaveValue('Draft B');

  const threadB = threadRow(page, 'Thread B');
  await threadB.hover();
  await threadB.locator('button[aria-label="Thread actions"]').click({ force: true });
  await page.getByRole('menuitem', { name: 'Delete' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();

  await expect(threadRow(page, 'Thread B')).toHaveCount(0);

  await page.getByRole('button', { name: 'New thread' }).first().click();
  await expect(threadRow(page, 'Thread B')).toBeVisible();
  await threadRow(page, 'Thread B').click();
  await expect(composer).toHaveValue('');
});

test('thread binding and run-scoped progress keys are explicitly encoded in source', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const source = await page.evaluate(async () => {
    const useChatSource = await fetch('/hooks/useChat.ts').then((res) => res.text());
    const assistantSource = await fetch('/components/chat/AssistantMessage.tsx').then((res) => res.text());
    return { useChatSource, assistantSource };
  });

  expect(source.useChatSource).toContain('if (!existingRun) return sendMessage(trimmed, { threadId });');
  expect(source.useChatSource).toContain('_uiRunId = runId');
  expect(source.useChatSource).toContain('return `${run}:${call}`;');
  expect(source.assistantSource).toContain('const progressKey = uiRunId && toolCall.id ?');
  expect(source.assistantSource).toContain('`${uiRunId}:${toolCall.id}`');
});
