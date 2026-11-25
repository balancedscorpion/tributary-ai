import { expect, test, type Page } from '@playwright/test';

function jsonResponse(body: any, status = 200) {
  return {
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  };
}

const settingsWithHighchartsDisabled = {
  provider: {
    providerId: 'compatible',
    model: 'test-model',
    profileId: 'custom',
  },
  featureFlags: {
    activation: true,
    providers: {
      openai: true,
      anthropic: true,
      gemini: true,
      bedrock: true,
      compatible: true,
    },
    providerTemplates: true,
    chartDimensionFilters: false,
    cloudCredentialsAws: false,
    cloudCredentialsTrino: false,
    cloudCredentialsSnowflake: false,
    chartEngines: {
      echarts: true,
      highcharts: false,
    },
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
      await route.fulfill(jsonResponse({ settings: settingsWithHighchartsDisabled }));
      return;
    }
    if (pathname.startsWith('/api/settings') && (method === 'PUT' || method === 'POST')) {
      await route.fulfill(jsonResponse({ settings: settingsWithHighchartsDisabled }));
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

test('Highcharts artifacts remain viewable when Highcharts feature flag is off', async ({ page }) => {
  const toolCall = {
    id: 'tc-highcharts-compat',
    name: 'display_table',
    args: {
      title: 'Legacy Highcharts Artifact',
      data_json: JSON.stringify([
        { month: '2026-01', value: 5 },
        { month: '2026-02', value: 7 },
      ]),
      chart_spec_version: 'hc-cap-v2-test',
      chart_spec: {
        schema_version: 'highcharts-plot-spec.v2',
        module: 'core',
        options: {
          title: { text: 'Legacy Highcharts Artifact' },
          series: [
            {
              type: 'line',
              name: 'Value',
              data: [5, 7],
            },
          ],
        },
      },
    },
  };

  await installMockApi(page, 'thread-highcharts-disabled-compat', toolCall);
  await page.goto('/');

  await expect(page.getByText('Legacy Highcharts Artifact').first()).toBeVisible();
  await page.getByText('Legacy Highcharts Artifact').first().click();
  await expect(page.getByTestId('highcharts-visualization')).toBeVisible();
});
