import { expect, test } from '@playwright/test';

test('compatible endpoint helpers split and rebuild full endpoint URLs', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const endpoint = await import('/lib/compatibleEndpointUrl.ts' as string);

    return {
      splitAbsolute: endpoint.splitCompatibleEndpointFromUrl('https://example.com/openai/v1/responses/beta'),
      splitRelative: endpoint.splitCompatibleEndpointFromUrl('/proxy/openai/v1/chat/completions/extra'),
      builtAbsolute: endpoint.buildCompatibleEndpointUrl({
        baseUrl: 'https://example.com/openai/v1',
        apiType: 'responses',
        endpointSuffix: '/beta',
        queryParams: { stream: 'false' },
      }),
      builtRelative: endpoint.buildCompatibleEndpointUrl({
        baseUrl: '/proxy/openai/v1',
        apiType: 'chat_completions',
        model: 'gpt-4.1-mini',
        modelInPath: true,
        endpointSuffix: '/tail',
      }),
    };
  });

  expect(result.splitAbsolute).toEqual({
    baseUrl: 'https://example.com/openai/v1',
    apiType: 'responses',
    endpointSuffix: '/beta',
    includesEndpoint: true,
  });
  expect(result.splitRelative).toEqual({
    baseUrl: '/proxy/openai/v1',
    apiType: 'chat_completions',
    endpointSuffix: '/extra',
    includesEndpoint: true,
  });
  expect(result.builtAbsolute).toBe('https://example.com/openai/v1/responses/beta?stream=false');
  expect(result.builtRelative).toBe('/proxy/openai/v1/gpt-4.1-mini/chat/completions/tail');
});

test('settings endpoint preview matches compatible runtime URL composition', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const runtime = await import('/lib/providerConfigRuntime.ts' as string);

    return {
      composed: runtime.buildProviderEndpoint('compatible', {
        id: 'cfg-compatible',
        displayName: 'Custom Provider',
        providerId: 'compatible',
        model: 'gpt-4.1-mini',
        apiType: 'responses',
        baseUrl: 'https://example.com/openai/v1',
        endpointSuffix: '/beta',
        queryParams: { stream: 'false' },
        modelInPath: false,
      }),
      preservedExistingEndpoint: runtime.buildProviderEndpoint('compatible', {
        id: 'cfg-compatible-2',
        displayName: 'Custom Provider',
        providerId: 'compatible',
        model: 'gpt-4.1-mini',
        apiType: 'chat_completions',
        baseUrl: 'https://example.com/custom/chat/completions',
        endpointSuffix: '/tail',
        modelInPath: true,
      }),
    };
  });

  expect(result.composed).toBe('https://example.com/openai/v1/responses/beta?stream=false');
  expect(result.preservedExistingEndpoint).toBe('https://example.com/custom/chat/completions/tail');
});
