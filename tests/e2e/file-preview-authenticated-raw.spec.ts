import { expect, test, type Page, type Route } from '@playwright/test';

/**
 * Proves uploaded-file previews load their bytes from /api/artifacts/{id}/raw
 * through the authenticated fetch shim (services/apiClient.ts) and never point
 * an <img src> / <iframe src> directly at an /api/ URL.
 *
 * Every test seeds the per-launch desktop token into sessionStorage before the
 * app boots, and the mocked raw endpoint replays the Go desktop-auth guard: a
 * request without X-AI-Desktop-Token gets the 401 DESKTOP_TOKEN_REQUIRED JSON
 * body. A direct <img>/<iframe> request would carry no token (the shim only
 * patches window.fetch), so a rendered preview is itself proof of the fetch
 * path — and the recorded resourceType/token per request pins it down.
 */

const THREAD_ID = 'thread-raw-preview';
const DESKTOP_TOKEN = 'ab'.repeat(32);
const TOKEN_STORAGE_KEY = 'ai_analyst_desktop_token_v1';
const TOKEN_HEADER = 'x-ai-desktop-token';

const IMAGE_ID = 'artifact-raw-image';
const PDF_ID = 'artifact-raw-pdf';
const HTML_ID = 'artifact-raw-html';
const TEXT_ID = 'artifact-raw-text';

const TEXT_PREVIEW = 'preview text only - not the stored bytes';
const RAW_TEXT_BYTES = 'RAW TEXT BYTES served by the raw endpoint';
const RAW_HTML_HEADING = 'Raw HTML served by the raw endpoint';
const RAW_HTML_BYTES = `<html><body><h1>${RAW_HTML_HEADING}</h1></body></html>`;
// Smallest valid PNG (1x1, transparent).
const RAW_PNG_BYTES = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
  'base64',
);
// The card-level PDF preview only object-URLs the bytes into an iframe, so a
// stub body is enough to prove the transport path.
const RAW_PDF_BYTES = Buffer.from('%PDF-1.4 stub bytes for transport test');

function jsonResponse(body: unknown, status = 200) {
  return {
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  };
}

const mockSettingsPayload = {
  provider: { providerId: 'compatible', model: 'test-model', profileId: 'custom' },
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

/** Uploaded-file artifact as the thread endpoint hydrates it (chat_upload). */
function uploadArtifact(id: string, title: string, fileKind: string, mimeType: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    type: 'file',
    title,
    content: '',
    metadata: {
      fileName: title,
      fileKind,
      mimeType,
      normalizedMimeType: mimeType,
      source: 'chat_upload',
      previewAvailable: true,
      ...extra,
    },
  };
}

const imageArtifact = uploadArtifact(IMAGE_ID, 'photo.png', 'image', 'image/png', { sizeBytes: RAW_PNG_BYTES.length });
const pdfArtifact = uploadArtifact(PDF_ID, 'report.pdf', 'pdf', 'application/pdf', { sizeBytes: RAW_PDF_BYTES.length });
const htmlArtifact = uploadArtifact(HTML_ID, 'page.html', 'html', 'text/html', { sizeBytes: RAW_HTML_BYTES.length });
const textArtifact = {
  ...uploadArtifact(TEXT_ID, 'notes.txt', 'text', 'text/plain', {
    sizeBytes: RAW_TEXT_BYTES.length,
    previewText: TEXT_PREVIEW,
  }),
  content: TEXT_PREVIEW,
};

const ALL_ARTIFACTS = [imageArtifact, pdfArtifact, htmlArtifact, textArtifact];

const RAW_BODIES: Record<string, { body: Buffer | string; contentType: string }> = {
  [IMAGE_ID]: { body: RAW_PNG_BYTES, contentType: 'image/png' },
  [PDF_ID]: { body: RAW_PDF_BYTES, contentType: 'application/pdf' },
  [HTML_ID]: { body: RAW_HTML_BYTES, contentType: 'text/html' },
  [TEXT_ID]: { body: RAW_TEXT_BYTES, contentType: 'text/plain' },
};

async function seedDesktopToken(page: Page): Promise<void> {
  await page.addInitScript(
    ([key, token]) => {
      window.sessionStorage.setItem(key, token);
    },
    [TOKEN_STORAGE_KEY, DESKTOP_TOKEN] as const,
  );
}

/** Boots the app with one thread whose user message carries the file artifacts. */
async function installTranscriptApi(page: Page): Promise<void> {
  const now = Date.now();
  const thread = {
    id: THREAD_ID,
    title: 'Raw Preview Thread',
    created_at: now - 1000,
    updated_at: now - 1000,
  };
  const message = {
    id: 'msg-user-1',
    role: 'user',
    content: 'Uploaded files',
    referenced_artifact_ids: ALL_ARTIFACTS.map((artifact) => artifact.id),
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

    if (pathname === `/api/threads/${THREAD_ID}` && method === 'GET') {
      await route.fulfill(jsonResponse({ thread, messages: [message], artifacts: ALL_ARTIFACTS }));
      return;
    }

    const artifactMatch = pathname.match(/^\/api\/artifacts\/([^/]+)$/);
    if (artifactMatch && method === 'GET') {
      const artifact = ALL_ARTIFACTS.find((candidate) => candidate.id === decodeURIComponent(artifactMatch[1]));
      if (artifact) {
        await route.fulfill(jsonResponse({ artifact }));
        return;
      }
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
      await route.fulfill(
        jsonResponse({
          initialized: true,
          locked: false,
          configured: {
            openai: true,
            anthropic: true,
            gemini: true,
            compatible: { default: { apiKey: true } },
          },
        }),
      );
      return;
    }

    if (pathname.startsWith('/api/')) {
      await route.fulfill(jsonResponse({}));
      return;
    }

    await route.continue();
  });
}

interface RawRequestLog {
  artifactId: string;
  resourceType: string;
  hasToken: boolean;
}

/**
 * Serves raw bytes only to token-carrying requests; everything else gets the
 * backend guard's 401 DESKTOP_TOKEN_REQUIRED JSON, exactly like a desktop
 * window whose <img>/<iframe> requests bypass the fetch shim.
 */
async function installGuardedRawRoute(page: Page): Promise<RawRequestLog[]> {
  const requests: RawRequestLog[] = [];
  await page.route('**/api/artifacts/*/raw*', async (route: Route) => {
    const req = route.request();
    const pathname = new URL(req.url()).pathname;
    const artifactId = decodeURIComponent(pathname.match(/^\/api\/artifacts\/([^/]+)\/raw$/)?.[1] || '');
    const token = req.headers()[TOKEN_HEADER];
    const hasToken = token === DESKTOP_TOKEN;
    requests.push({ artifactId, resourceType: req.resourceType(), hasToken });

    if (!hasToken) {
      await route.fulfill(
        jsonResponse({ ok: false, code: 'DESKTOP_TOKEN_REQUIRED', message: 'desktop token required' }, 401),
      );
      return;
    }

    const raw = RAW_BODIES[artifactId];
    if (!raw) {
      await route.fulfill(jsonResponse({ error: 'unknown artifact' }, 404));
      return;
    }
    await route.fulfill({
      status: 200,
      body: raw.body,
      headers: {
        'content-type': raw.contentType,
        'content-disposition': 'inline',
        'x-content-type-options': 'nosniff',
      },
    });
  });
  return requests;
}

async function gotoTranscript(page: Page): Promise<void> {
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(page.getByText('Uploaded files')).toBeVisible();
}

test('transcript cards load image, PDF, and HTML previews via authenticated fetch only', async ({ page }) => {
  await seedDesktopToken(page);
  await installTranscriptApi(page);
  const rawRequests = await installGuardedRawRoute(page);

  await gotoTranscript(page);

  // Image card: thumbnail is fetched then rendered from an object URL.
  const thumbnail = page.locator('img[alt="Preview of photo.png"]');
  await expect(thumbnail).toBeVisible();
  expect(await thumbnail.getAttribute('src')).toMatch(/^blob:/);

  // PDF card: bytes are fetched then shown through a blob-URL iframe.
  const pdfFrame = page.locator('iframe[data-file-preview-kind="pdf"]');
  await expect(pdfFrame).toBeVisible();
  expect(await pdfFrame.getAttribute('src')).toMatch(/^blob:/);

  // HTML card: markup is fetched and rendered via srcDoc (no src at all).
  const htmlFrame = page.locator('iframe[data-file-preview-kind="html"]');
  await expect(htmlFrame).toBeVisible();
  expect(await htmlFrame.getAttribute('src')).toBeNull();
  await expect(page.frameLocator('iframe[data-file-preview-kind="html"]').getByText(RAW_HTML_HEADING)).toBeVisible();

  // Every raw request was a programmatic fetch carrying the desktop token —
  // never an <img>/<iframe> subresource request (which would log as
  // "image"/"document" and carry no token).
  const coveredIds = new Set(rawRequests.map((request) => request.artifactId));
  expect(coveredIds).toEqual(new Set([IMAGE_ID, PDF_ID, HTML_ID]));
  for (const request of rawRequests) {
    expect(['fetch', 'xhr']).toContain(request.resourceType);
    expect(request.hasToken).toBe(true);
  }

  // The guard's JSON error body never leaks into the UI.
  await expect(page.getByText('DESKTOP_TOKEN_REQUIRED')).toHaveCount(0);
});

test('native viewer renders an uploaded text file from authenticated raw bytes', async ({ page }) => {
  await seedDesktopToken(page);
  await installTranscriptApi(page);
  const rawRequests = await installGuardedRawRoute(page);

  await gotoTranscript(page);
  await page.getByRole('button', { name: 'Read text notes.txt' }).click();

  // The viewer shows the raw endpoint's bytes, not the card preview text —
  // proof it fetched /raw before rendering.
  const viewer = page.locator('[data-file-preview-kind="text-full"]');
  await expect(viewer).toBeVisible();
  await expect(viewer).toContainText(RAW_TEXT_BYTES);

  const textRequests = rawRequests.filter((request) => request.artifactId === TEXT_ID);
  expect(textRequests.length).toBeGreaterThan(0);
  for (const request of textRequests) {
    expect(['fetch', 'xhr']).toContain(request.resourceType);
    expect(request.hasToken).toBe(true);
  }
  await expect(page.getByText('DESKTOP_TOKEN_REQUIRED')).toHaveCount(0);
});

test('native viewer renders an uploaded image from fetched bytes as an object URL', async ({ page }) => {
  await seedDesktopToken(page);
  await installTranscriptApi(page);
  const rawRequests = await installGuardedRawRoute(page);

  await gotoTranscript(page);
  await page.getByRole('button', { name: 'Preview image photo.png' }).click();

  const viewerImage = page.locator('img[alt="photo.png"]');
  await expect(viewerImage).toBeVisible();
  expect(await viewerImage.getAttribute('src')).toMatch(/^blob:/);

  for (const request of rawRequests) {
    expect(['fetch', 'xhr']).toContain(request.resourceType);
    expect(request.hasToken).toBe(true);
  }
  await expect(page.getByText('DESKTOP_TOKEN_REQUIRED')).toHaveCount(0);
});

test('an unauthenticated raw endpoint degrades to preview fallbacks, never JSON in the UI', async ({ page }) => {
  // No token seeded: the shim has nothing to attach, so the guard 401s every
  // raw request. Previews must fall back gracefully — the JSON error body must
  // never render as preview content.
  await installTranscriptApi(page);
  await installGuardedRawRoute(page);

  await gotoTranscript(page);

  await expect(page.getByText('Preview unavailable. The image may be unsupported or failed to load.')).toBeVisible();
  await expect(page.getByText('Preview unavailable. The PDF file could not be loaded for rendering.')).toBeVisible();
  await expect(page.getByText('Preview unavailable. The HTML file could not be loaded for rendering.')).toBeVisible();
  await expect(page.getByText('DESKTOP_TOKEN_REQUIRED')).toHaveCount(0);
  await expect(page.getByText('desktop token required')).toHaveCount(0);
});
