import { expect, test, type Page } from '@playwright/test';

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

async function installMockApi(page: Page, threadId: string, toolCall: any): Promise<void> {
  const now = Date.now();
  const thread = {
    id: threadId,
    title: `Thread ${threadId}`,
    created_at: now - 1000,
    updated_at: now - 1000,
  };

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
      await route.fulfill(jsonResponse({
        thread,
        messages: [
          {
            id: `msg-${threadId}`,
            seq: 1,
            role: 'assistant',
            content: '',
            tool_calls: [toolCall],
          },
        ],
      }));
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

    if (pathname.startsWith('/api/')) {
      await route.fulfill(jsonResponse({}));
      return;
    }

    await route.continue();
  });
}

test('Artifact panel renders stage-2 maps plot spec with inline geometry', async ({ page }) => {
  const toolCall = {
    id: 'tc-stage2-maps',
    name: 'display_table',
    args: {
      title: 'Map Coverage',
      data_json: JSON.stringify([
        { region: 'A', value: 12 },
        { region: 'B', value: 7 },
      ]),
      chart_spec_version: 'hc-cap-v2-test',
      chart_spec: {
        schema_version: 'highcharts-plot-spec.v2',
        module: 'maps',
        modules: ['drilldown'],
        options: {
          title: { text: 'Map Coverage' },
          series: [
            {
              type: 'map',
              name: 'Coverage',
              mapData: {
                type: 'FeatureCollection',
                features: [],
              },
              data: [
                { 'hc-key': 'a', value: 12 },
                { 'hc-key': 'b', value: 7 },
              ],
            },
          ],
        },
      },
    },
  };

  await installMockApi(page, 'thread-highcharts-stage2-maps', toolCall);
  await page.goto('/');

  await expect(page.getByText('Map Coverage').first()).toBeVisible();
  await page.getByText('Map Coverage').first().click();

  const chart = page.getByTestId('highcharts-visualization');
  await expect(chart).toBeVisible();
  await expect(chart).toContainText('Map Coverage');
});

test('Artifact panel renders stage-2 gantt plot spec', async ({ page }) => {
  const toolCall = {
    id: 'tc-stage2-gantt',
    name: 'display_table',
    args: {
      title: 'Delivery Timeline',
      data_json: JSON.stringify([
        { task: 'Planning', start: Date.UTC(2026, 0, 1), end: Date.UTC(2026, 0, 5) },
        { task: 'Build', start: Date.UTC(2026, 0, 6), end: Date.UTC(2026, 0, 20) },
      ]),
      chart_spec_version: 'hc-cap-v2-test',
      chart_spec: {
        schema_version: 'highcharts-plot-spec.v2',
        module: 'gantt',
        modules: ['xrange'],
        options: {
          title: { text: 'Delivery Timeline' },
          series: [
            {
              type: 'gantt',
              name: 'Phases',
              data: [
                { id: 'plan', name: 'Planning', start: Date.UTC(2026, 0, 1), end: Date.UTC(2026, 0, 5) },
                { id: 'build', name: 'Build', start: Date.UTC(2026, 0, 6), end: Date.UTC(2026, 0, 20), dependency: 'plan' },
              ],
            },
          ],
        },
      },
    },
  };

  await installMockApi(page, 'thread-highcharts-stage2-gantt', toolCall);
  await page.goto('/');

  await expect(page.getByText('Delivery Timeline').first()).toBeVisible();
  await page.getByText('Delivery Timeline').first().click();

  const chart = page.getByTestId('highcharts-visualization');
  await expect(chart).toBeVisible();
  await expect(chart).toContainText('Delivery Timeline');
});
