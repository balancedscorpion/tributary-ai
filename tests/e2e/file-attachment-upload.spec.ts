import { expect, test, type Page, type Route } from '@playwright/test';

/**
 * End-to-end coverage for the composer file-upload lifecycle.
 *
 * The composer routes every attached file through the first-party upload API
 * (POST /api/threads/:id/attachments, sent over XMLHttpRequest). These tests
 * drive the hidden paperclip <input> with page.setInputFiles and exercise the
 * three states a pending file card moves through: uploading → uploaded, and the
 * error → retry → uploaded recovery path. Backend routes are mocked with
 * page.route (which also intercepts the XHR upload).
 */

const THREAD_ID = 'thread-upload';
const UPLOAD_FILE_NAME = 'notes.txt';
const UPLOAD_TEXT = 'hello world from the upload spec';

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

/**
 * Realistic {artifact, attachment} payload matching the shape the Go handler
 * returns (server/internal/chat/httpapi/thread_attachments.go). The text/plain
 * mime resolves to the "Text" meta label and a "text" card kind so the compact
 * card renders its inline preview.
 */
function attachmentResponse() {
  const artifactId = 'artifact-upload-1';
  const metadata = {
    previewText: UPLOAD_TEXT,
    previewAvailable: true,
    fileKind: 'text',
    mimeType: 'text/plain',
  };
  return {
    artifact: {
      id: artifactId,
      type: 'file',
      title: UPLOAD_FILE_NAME,
      content: UPLOAD_TEXT,
      metadata,
    },
    attachment: {
      artifactId,
      fileName: UPLOAD_FILE_NAME,
      mimeType: 'text/plain',
      detectedMimeType: 'text/plain',
      normalizedMimeType: 'text/plain',
      fileKind: 'text',
      sizeBytes: UPLOAD_TEXT.length,
      isText: true,
      previewText: UPLOAD_TEXT,
      previewAvailable: true,
      previewTruncated: false,
      source: 'upload',
    },
  };
}

/**
 * Boots the app with a single existing thread so the composer mounts with an
 * active thread (no thread-create round-trip needed). Attachment uploads are
 * intentionally left unhandled here; each test registers its own attachments
 * route afterwards so it takes routing precedence (Playwright checks the most
 * recently registered route first).
 */
async function installBaseApi(page: Page): Promise<void> {
  const now = Date.now();
  const thread = {
    id: THREAD_ID,
    title: 'Upload Thread',
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

    if (pathname === '/api/threads' && method === 'POST') {
      await route.fulfill(jsonResponse({ thread }));
      return;
    }

    if (pathname === `/api/threads/${THREAD_ID}` && method === 'GET') {
      await route.fulfill(jsonResponse({ thread, messages: [], artifacts: [] }));
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

/**
 * The paperclip attachment input is the file input with neither an accept
 * filter (rules out the image / import-thread inputs) nor webkitdirectory
 * (rules out the header's folder picker).
 */
function attachmentInput(page: Page) {
  return page.locator('input[type="file"]:not([accept]):not([webkitdirectory])');
}

function attachTextFile(page: Page) {
  return attachmentInput(page).setInputFiles({
    name: UPLOAD_FILE_NAME,
    mimeType: 'text/plain',
    buffer: Buffer.from(UPLOAD_TEXT),
  });
}

/** Single send button whose accessible name always starts with "Send message". */
function sendButton(page: Page) {
  return page.getByRole('button', { name: /Send message/ });
}

async function gotoComposer(page: Page) {
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(page.locator('textarea').first()).toBeVisible();
}

test('uploads a file and renders its preview once the attachment resolves', async ({ page }) => {
  await installBaseApi(page);
  await page.route('**/api/threads/*/attachments*', async (route: Route) => {
    await route.fulfill(jsonResponse(attachmentResponse()));
  });

  await gotoComposer(page);
  await attachTextFile(page);

  // The pending card appears immediately with the file name. (exact: the
  // sr-only announcer also mentions the file name in a longer sentence.)
  await expect(page.getByText(UPLOAD_FILE_NAME, { exact: true })).toBeVisible();

  // Once the mocked upload resolves, the card shows the preview text and the
  // full meta line ("Text · 32 B · 1 line"). Matching the whole line avoids a
  // strict-mode collision with other "Text" occurrences on the card (the
  // "Text preview" footer note and the "Download text" action).
  await expect(page.getByText(UPLOAD_TEXT)).toBeVisible();
  await expect(page.getByText(`Text · ${UPLOAD_TEXT.length} B · 1 line`)).toBeVisible();
  await expect(sendButton(page)).toBeEnabled();
});

test('surfaces an upload failure with a retry that recovers to the uploaded state', async ({ page }) => {
  await installBaseApi(page);
  let attempts = 0;
  await page.route('**/api/threads/*/attachments*', async (route: Route) => {
    attempts += 1;
    if (attempts === 1) {
      await route.fulfill(jsonResponse({ error: 'boom' }, 500));
      return;
    }
    await route.fulfill(jsonResponse(attachmentResponse()));
  });

  await gotoComposer(page);
  await attachTextFile(page);

  // First attempt fails: the card becomes an alert with the error and a retry.
  const errorCard = page.getByRole('alert');
  await expect(errorCard).toBeVisible();
  await expect(errorCard.getByText('boom')).toBeVisible();
  const retry = page.getByRole('button', { name: `Retry uploading ${UPLOAD_FILE_NAME}` });
  await expect(retry).toBeVisible();

  // Retry drives a second upload that succeeds and hydrates the preview.
  await retry.click();
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByText(UPLOAD_TEXT)).toBeVisible();
  await expect(sendButton(page)).toBeEnabled();
  expect(attempts).toBe(2);
});

test('blocks sending while an upload is in flight and unblocks when it settles', async ({ page }) => {
  await installBaseApi(page);
  let releaseUpload: () => void = () => {};
  const uploadGate = new Promise<void>((resolve) => {
    releaseUpload = resolve;
  });
  await page.route('**/api/threads/*/attachments*', async (route: Route) => {
    await uploadGate;
    await route.fulfill(jsonResponse(attachmentResponse()));
  });

  await gotoComposer(page);
  await attachTextFile(page);

  // While the upload is pending: the waiting hint shows and send is disabled.
  await expect(page.getByText(/Waiting for upload to finish/)).toBeVisible();
  await expect(sendButton(page)).toBeDisabled();

  // Resolve the upload: the hint clears and send becomes available.
  releaseUpload();
  await expect(page.getByText(/Waiting for upload to finish/)).toHaveCount(0);
  await expect(sendButton(page)).toBeEnabled();
});
