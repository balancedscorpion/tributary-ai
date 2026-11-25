import { readFileSync } from 'node:fs';
import { expect, test, type Page, type Route } from '@playwright/test';

/**
 * End-to-end coverage for the transcript file-card download action.
 *
 * Uploaded-file artifacts keep only preview text in `artifact.content`; the
 * original bytes live behind GET /api/artifacts/{id}/raw. The download control
 * must fetch that endpoint programmatically (fetch → Blob → object-URL anchor)
 * and save under the real filename — it must never navigate the page to the
 * raw URL. These tests serve DIFFERENT bytes from /raw than the preview text
 * shown in the card, so a passing download proves the raw path is used.
 */

const THREAD_ID = 'thread-download';
const ARTIFACT_ID = 'artifact-download-1';
const FILE_NAME = 'notes.txt';
const PREVIEW_TEXT = 'preview text only - not the stored bytes';
const RAW_BYTES = 'RAW ORIGINAL BYTES served by /api/artifacts/{id}/raw';

test.use({ acceptDownloads: true });

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

/** Message-level snapshot persisted alongside the user message. */
const attachmentSnapshot = {
  artifactId: ARTIFACT_ID,
  fileName: FILE_NAME,
  mimeType: 'text/plain',
  detectedMimeType: 'text/plain',
  fileKind: 'text',
  sizeBytes: RAW_BYTES.length,
  previewText: PREVIEW_TEXT,
  previewAvailable: true,
  previewTruncated: false,
  source: 'upload',
};

/**
 * Hydrated file-attachment artifact as the thread endpoint returns it
 * (fileAttachmentArtifactDTO): preview text as content, chat_upload source.
 */
const fileArtifact = {
  id: ARTIFACT_ID,
  type: 'file',
  title: FILE_NAME,
  content: PREVIEW_TEXT,
  metadata: {
    fileName: FILE_NAME,
    previewText: PREVIEW_TEXT,
    previewAvailable: true,
    fileKind: 'text',
    mimeType: 'text/plain',
    normalizedMimeType: 'text/plain',
    sizeBytes: RAW_BYTES.length,
    source: 'chat_upload',
  },
};

/**
 * Boots the app with one thread whose transcript contains a user message
 * carrying a file attachment. Artifact routes are intentionally left to each
 * test (registered afterwards, so they take routing precedence).
 */
async function installTranscriptApi(
  page: Page,
  opts: { message: Record<string, unknown>; artifacts: unknown[] },
): Promise<void> {
  const now = Date.now();
  const thread = {
    id: THREAD_ID,
    title: 'Download Thread',
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

    if (pathname === `/api/threads/${THREAD_ID}` && method === 'GET') {
      await route.fulfill(
        jsonResponse({ thread, messages: [opts.message], artifacts: opts.artifacts }),
      );
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
  resourceType: string;
  url: string;
}

/** Serves the raw bytes exactly like the Go endpoint (inline disposition). */
async function installRawRoute(page: Page, status = 200): Promise<RawRequestLog[]> {
  const requests: RawRequestLog[] = [];
  await page.route(`**/api/artifacts/${ARTIFACT_ID}/raw*`, async (route: Route) => {
    requests.push({
      resourceType: route.request().resourceType(),
      url: route.request().url(),
    });
    if (status !== 200) {
      await route.fulfill(jsonResponse({ error: 'boom' }, status));
      return;
    }
    await route.fulfill({
      status: 200,
      body: RAW_BYTES,
      headers: {
        'content-type': 'text/plain',
        'content-disposition': `inline; filename="${FILE_NAME}"`,
        'x-content-type-options': 'nosniff',
      },
    });
  });
  return requests;
}

function downloadButton(page: Page) {
  return page.getByRole('button', { name: `Download ${FILE_NAME}` });
}

test('transcript file card downloads the original raw bytes without navigating', async ({ page }) => {
  await installTranscriptApi(page, {
    message: {
      id: 'msg-user-1',
      role: 'user',
      content: 'Here is the report',
      referenced_artifact_ids: [ARTIFACT_ID],
      file_attachments: [attachmentSnapshot],
    },
    artifacts: [fileArtifact],
  });
  await page.route(`**/api/artifacts/${ARTIFACT_ID}`, async (route: Route) => {
    await route.fulfill(jsonResponse({ artifact: fileArtifact }));
  });
  const rawRequests = await installRawRoute(page);

  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(downloadButton(page)).toBeVisible();
  const urlBeforeClick = page.url();

  const downloadPromise = page.waitForEvent('download');
  await downloadButton(page).click();
  const download = await downloadPromise;

  // Real filename, real bytes (not the preview text rendered in the card).
  expect(download.suggestedFilename()).toBe(FILE_NAME);
  const downloadPath = await download.path();
  expect(downloadPath).toBeTruthy();
  expect(readFileSync(downloadPath!, 'utf8')).toBe(RAW_BYTES);

  // The raw endpoint was fetched programmatically — not visited as a page.
  expect(rawRequests).toHaveLength(1);
  expect(rawRequests[0].resourceType).toBe('fetch');
  expect(page.url()).toBe(urlBeforeClick);
  await expect(downloadButton(page)).toBeVisible();
});

test('snapshot-only attachment (no hydrated artifact) downloads via its artifactId', async ({ page }) => {
  await installTranscriptApi(page, {
    message: {
      id: 'msg-user-1',
      role: 'user',
      content: 'Snapshot only',
      file_attachments: [attachmentSnapshot],
    },
    artifacts: [],
  });
  const rawRequests = await installRawRoute(page);

  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(downloadButton(page)).toBeVisible();
  const urlBeforeClick = page.url();

  const downloadPromise = page.waitForEvent('download');
  await downloadButton(page).click();
  const download = await downloadPromise;

  expect(download.suggestedFilename()).toBe(FILE_NAME);
  expect(rawRequests).toHaveLength(1);
  expect(rawRequests[0].resourceType).toBe('fetch');
  expect(page.url()).toBe(urlBeforeClick);
});

test('failed raw download surfaces a toast and stays on the page', async ({ page }) => {
  await installTranscriptApi(page, {
    message: {
      id: 'msg-user-1',
      role: 'user',
      content: 'Broken download',
      file_attachments: [attachmentSnapshot],
    },
    artifacts: [],
  });
  const rawRequests = await installRawRoute(page, 500);

  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(downloadButton(page)).toBeVisible();
  const urlBeforeClick = page.url();

  await downloadButton(page).click();

  await expect(page.getByText(`Couldn't download ${FILE_NAME}.`)).toBeVisible();
  expect(rawRequests).toHaveLength(1);
  expect(page.url()).toBe(urlBeforeClick);
});
