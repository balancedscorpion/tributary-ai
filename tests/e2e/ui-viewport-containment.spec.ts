import { expect, test, type Page } from '@playwright/test';

const THREAD_ID = 'thread-viewport-containment';
const longToken = 'viewport_overflow_regression_token_'.repeat(120);

function jsonResponse(body: unknown, status = 200) {
  return {
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  };
}

function mockMessages() {
  return Array.from({ length: 48 }, (_, index) => ({
    id: `message-${index}`,
    seq: index + 1,
    role: index % 2 === 0 ? 'user' : 'assistant',
    content:
      index === 1
        ? `A deliberately unbroken response must stay inside the transcript: ${longToken}`
        : `Viewport containment message ${index + 1}. `.repeat(8),
    created_at: Date.now() - (48 - index) * 1000,
  }));
}

async function installMockApi(page: Page): Promise<void> {
  const now = Date.now();
  const thread = {
    id: THREAD_ID,
    title: 'Viewport containment',
    created_at: now - 60_000,
    updated_at: now,
  };
  const settings = {
    provider: {
      providerId: 'compatible',
      model: 'viewport-test-model',
      profileId: 'custom',
    },
    compatibleProfiles: [],
    providerConfigs: [],
    featureFlags: { activation: true },
  };

  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const method = request.method().toUpperCase();

    if (url.pathname === '/api/threads' && method === 'GET') {
      await route.fulfill(jsonResponse({ threads: [thread] }));
      return;
    }
    if (url.pathname === `/api/threads/${THREAD_ID}` && method === 'GET') {
      await route.fulfill(jsonResponse({ thread, messages: mockMessages() }));
      return;
    }
    if (url.pathname.startsWith('/api/settings') && method === 'GET') {
      await route.fulfill(jsonResponse({ settings }));
      return;
    }
    if (url.pathname === '/api/secrets/status' && method === 'GET') {
      await route.fulfill(jsonResponse({ initialized: true, locked: false, configured: {} }));
      return;
    }
    await route.fulfill(jsonResponse({}));
  });
}

async function openContainedApp(page: Page, width: number, height: number): Promise<void> {
  await page.setViewportSize({ width, height });
  await installMockApi(page);
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(page.getByText('Viewport containment message 48.', { exact: false })).toBeVisible();
}

async function readContainment(page: Page) {
  return page.evaluate(() => {
    const html = document.documentElement;
    const body = document.body;
    const root = document.getElementById('root');
    const shell = document.querySelector<HTMLElement>('[data-app-shell]');
    const header = document.querySelector<HTMLElement>('header');
    const transcript = document.querySelector<HTMLElement>('[data-radix-scroll-area-viewport]');
    const headerButtonRects = header
      ? Array.from(header.querySelectorAll('button')).map((button) => {
          const rect = button.getBoundingClientRect();
          return {
            name: button.getAttribute('aria-label') || button.textContent?.trim() || '',
            left: rect.left,
            right: rect.right,
          };
        })
      : [];

    return {
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      html: {
        clientWidth: html.clientWidth,
        scrollWidth: html.scrollWidth,
        clientHeight: html.clientHeight,
        scrollHeight: html.scrollHeight,
        scrollTop: html.scrollTop,
        overflow: getComputedStyle(html).overflow,
      },
      body: {
        clientWidth: body.clientWidth,
        scrollWidth: body.scrollWidth,
        clientHeight: body.clientHeight,
        scrollHeight: body.scrollHeight,
        scrollTop: body.scrollTop,
        overflow: getComputedStyle(body).overflow,
      },
      root: root && {
        clientWidth: root.clientWidth,
        scrollWidth: root.scrollWidth,
        clientHeight: root.clientHeight,
        scrollHeight: root.scrollHeight,
      },
      shell: shell && {
        clientWidth: shell.clientWidth,
        scrollWidth: shell.scrollWidth,
        clientHeight: shell.clientHeight,
        scrollHeight: shell.scrollHeight,
      },
      header: header && {
        clientWidth: header.clientWidth,
        scrollWidth: header.scrollWidth,
      },
      transcript: transcript && {
        clientWidth: transcript.clientWidth,
        scrollWidth: transcript.scrollWidth,
        clientHeight: transcript.clientHeight,
        scrollHeight: transcript.scrollHeight,
        scrollTop: transcript.scrollTop,
        overflowX: getComputedStyle(transcript).overflowX,
        overscrollBehavior: getComputedStyle(transcript).overscrollBehavior,
      },
      headerButtonRects,
    };
  });
}

for (const viewport of [
  { width: 1280, height: 800 },
  { width: 768, height: 720 },
  { width: 320, height: 568 },
]) {
  test(`keeps the app shell and long transcript inside ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await openContainedApp(page, viewport.width, viewport.height);
    const metrics = await readContainment(page);

    expect(metrics.html.overflow).toBe('hidden');
    expect(metrics.body.overflow).toBe('hidden');
    expect(metrics.html.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.body.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.html.scrollHeight).toBeLessThanOrEqual(metrics.viewportHeight);
    expect(metrics.body.scrollHeight).toBeLessThanOrEqual(metrics.viewportHeight);
    expect(metrics.root?.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.shell?.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.shell?.scrollHeight).toBeLessThanOrEqual(metrics.viewportHeight);
    expect(metrics.header?.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.transcript?.scrollWidth).toBeLessThanOrEqual(metrics.transcript?.clientWidth || 0);
    expect(metrics.transcript?.scrollHeight).toBeGreaterThan(metrics.transcript?.clientHeight || 0);
    expect(metrics.transcript?.overflowX).toBe('hidden');
    expect(metrics.transcript?.overscrollBehavior).toBe('contain');

    for (const button of metrics.headerButtonRects) {
      expect(button.left, `${button.name} must not leave the viewport`).toBeGreaterThanOrEqual(0);
      expect(button.right, `${button.name} must not leave the viewport`).toBeLessThanOrEqual(metrics.viewportWidth);
    }
  });
}

test('scrolling the transcript never scrolls the whole application', async ({ page }) => {
  await openContainedApp(page, 768, 620);
  const transcript = page.locator('[data-radix-scroll-area-viewport]');
  await expect(transcript).toHaveCount(1);
  await transcript.evaluate((element) => {
    element.scrollTop = 0;
  });
  await transcript.hover();
  await page.mouse.wheel(0, 900);

  const metrics = await readContainment(page);
  expect(metrics.transcript?.scrollTop).toBeGreaterThan(0);
  expect(metrics.html.scrollTop).toBe(0);
  expect(metrics.body.scrollTop).toBe(0);
});

test('Command plus and Command minus change persistent text zoom without breaking containment', async ({ page }) => {
  await openContainedApp(page, 320, 568);
  const composer = page.getByPlaceholder('Send a message...', { exact: true });
  await composer.focus();
  await expect(page.locator('html')).toHaveAttribute('data-ui-text-zoom', '100');
  const defaultComposerFontSize = await composer.evaluate((element) =>
    Number.parseFloat(getComputedStyle(element).fontSize),
  );

  for (let index = 0; index < 4; index += 1) {
    await page.keyboard.press('Meta+Shift+Equal');
  }
  await expect(page.locator('html')).toHaveAttribute('data-ui-text-zoom', '140');
  await expect(page.locator('[data-app-shell]')).toHaveAttribute('data-ui-text-zoom', '140');
  const enlargedComposerFontSize = await composer.evaluate((element) =>
    Number.parseFloat(getComputedStyle(element).fontSize),
  );
  expect(enlargedComposerFontSize).toBeGreaterThan(defaultComposerFontSize * 1.35);
  expect(await composer.inputValue()).toBe('');

  await page.keyboard.press('Meta+Shift+Equal');
  await expect(page.locator('html')).toHaveAttribute('data-ui-text-zoom', '140');

  let metrics = await readContainment(page);
  expect(metrics.html.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
  expect(metrics.shell?.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
  expect(metrics.header?.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
  for (const button of metrics.headerButtonRects) {
    expect(button.left, `${button.name} must stay visible at 140%`).toBeGreaterThanOrEqual(0);
    expect(button.right, `${button.name} must stay visible at 140%`).toBeLessThanOrEqual(metrics.viewportWidth);
  }

  await page.keyboard.press('Meta+Minus');
  await expect(page.locator('html')).toHaveAttribute('data-ui-text-zoom', '130');
  expect(await composer.inputValue()).toBe('');

  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect(page.locator('html')).toHaveAttribute('data-ui-text-zoom', '130');
  metrics = await readContainment(page);
  expect(metrics.html.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth);
  expect(metrics.html.scrollHeight).toBeLessThanOrEqual(metrics.viewportHeight);

  await composer.focus();
  for (let index = 0; index < 6; index += 1) {
    await page.keyboard.press('Meta+Minus');
  }
  await expect(page.locator('html')).toHaveAttribute('data-ui-text-zoom', '80');
  await page.keyboard.press('Meta+Minus');
  await expect(page.locator('html')).toHaveAttribute('data-ui-text-zoom', '80');
});
