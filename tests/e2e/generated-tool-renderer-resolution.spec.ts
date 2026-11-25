import { expect, test } from '@playwright/test';

test('generated tool renderer representatives mount without falling back or crashing', async ({ page }) => {
  const browserErrors: string[] = [];
  page.on('pageerror', (error) => browserErrors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(message.text());
  });

  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const cases = await page.evaluate(async () => {
    const harness = await import('/tests/e2e/fixtures/generated-tool-renderer-resolution-harness.tsx' as string);
    return harness.mountGeneratedToolRendererResolutionHarness();
  });

  await expect(page.locator('[data-renderer-error]')).toHaveCount(0);
  expect(browserErrors).toEqual([]);

  for (const tc of cases) {
    const section = page.getByTestId(`renderer-case-${tc.id}`);
    await expect(section, `${tc.id} section should mount`).toBeVisible();
    await expect(section, `${tc.toolName} should show ${tc.expectedHeader}`).toContainText(tc.expectedHeader);
  }

  await expect(page.getByTestId('renderer-case-code')).toContainText('renderer-code-sentinel');
  await expect(page.getByTestId('renderer-case-file')).toContainText('renderer-file-sentinel');
  await expect(page.getByTestId('renderer-case-interactive').locator('[data-testid="sandboxed-html-renderer"]')).toBeVisible();
  await expect(page.getByTestId('renderer-case-query')).toContainText('select 1 as renderer_query_sentinel');
  await expect(page.getByTestId('renderer-case-catalog')).toContainText('catalog_schema_sentinel');
  await expect(page.getByTestId('renderer-case-connector')).toContainText('renderer-slack-sentinel');
});

test('every generated tool mounts through ArtifactPanel without renderer errors', async ({ page }) => {
  test.setTimeout(120000);
  const browserErrors: string[] = [];
  page.on('pageerror', (error) => browserErrors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(message.text());
  });

  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const cases = await page.evaluate(async () => {
    const harness = await import('/tests/e2e/fixtures/generated-tool-renderer-resolution-harness.tsx' as string);
    return harness.mountEveryGeneratedToolRendererHarness();
  });

  expect(cases.length).toBeGreaterThan(100);
  await expect(page.locator('[data-renderer-error]')).toHaveCount(0);
  expect(browserErrors).toEqual([]);

  for (const tc of cases) {
    const section = page.getByTestId(`renderer-all-case-${tc.id}`);
    await expect(section, `${tc.toolName} section should mount`).toBeVisible();
    if (
      tc.nativeViewer !== 'interactive_html' &&
      tc.nativeViewer !== 'image' &&
      tc.nativeViewer !== 'connector_inspector'
    ) {
      await expect(section, `${tc.toolName} should expose fixture content`).toContainText(tc.sentinel);
    }
  }

  const sharePointSearch = page.getByTestId('renderer-all-case-all-sharepoint_search');
  await expect(sharePointSearch).toContainText('sharepoint_search');
  await expect(sharePointSearch.getByLabel('SharePoint completed')).toBeVisible();
  await expect(sharePointSearch).toContainText('Request');
  await expect(sharePointSearch).toContainText('No arguments');
  await expect(sharePointSearch).toContainText('Response');
  await expect(sharePointSearch).toContainText('renderer-all-sharepoint_search-sentinel');
  await expect(sharePointSearch).toContainText('Metadata');
  await expect(sharePointSearch).toContainText('integration');
  await expect(sharePointSearch).toContainText('sharepoint');
  await expect(sharePointSearch).toContainText('Raw');
});

test('provider-hosted capabilities mount with provider-specific inspector identity', async ({ page }) => {
  const browserErrors: string[] = [];
  page.on('pageerror', (error) => browserErrors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(message.text());
  });

  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const cases = await page.evaluate(async () => {
    const harness = await import('/tests/e2e/fixtures/generated-tool-renderer-resolution-harness.tsx' as string);
    return harness.mountProviderHostedRendererHarness();
  });

  expect(cases).toHaveLength(4);
  await expect(page.locator('[data-renderer-error]')).toHaveCount(0);
  expect(browserErrors).toEqual([]);

  for (const tc of cases) {
    const section = page.getByTestId(`renderer-hosted-case-${tc.id}`);
    await expect(section, `${tc.toolName} section should mount`).toBeVisible();
    await expect(section, `${tc.toolName} should show provider identity`).toContainText(tc.expectedHeader);
    await expect(section, `${tc.toolName} should expose hosted output`).toContainText(tc.sentinel);
  }
});

test('every generated tool result routes through ToolResultBlock without renderer errors', async ({ page }) => {
  test.setTimeout(120000);
  const browserErrors: string[] = [];
  page.on('pageerror', (error) => browserErrors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(message.text());
  });

  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const cases = await page.evaluate(async () => {
    const harness = await import('/tests/e2e/fixtures/generated-tool-renderer-resolution-harness.tsx' as string);
    return harness.mountEveryGeneratedToolResultBlockHarness();
  });

  expect(cases.length).toBeGreaterThan(100);
  for (const hostedToolName of [
    'anthropic-messages/code_execution',
    'anthropic-messages/web_search',
    'openai-responses/code_interpreter',
    'openai-responses/web_search',
  ]) {
    expect(
      cases.some((tc) => tc.toolName === hostedToolName),
      `${hostedToolName} should be routed through ToolResultBlock`,
    ).toBe(true);
  }
  await expect(page.locator('[data-renderer-error]')).toHaveCount(0);
  expect(browserErrors).toEqual([]);

  for (const tc of cases) {
    const section = page.getByTestId(`tool-result-route-case-${tc.id}`);
    await expect(section, `${tc.toolName} route wrapper should mount`).toBeVisible();
    if (!tc.suppressed) {
      await expect(section, `${tc.toolName} should render through the chat tool-result route`).toContainText(tc.expectedText);
    }
  }
});
