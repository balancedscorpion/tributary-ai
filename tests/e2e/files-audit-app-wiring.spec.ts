import { expect, test, type Page } from '@playwright/test';

const THREAD_ID = 'thread-files-app';
const OTHER_THREAD_ID = 'thread-files-other';

function markdownArtifact(id: string, title: string, sourceLabel: string, toolName: string, body: string, extraMetadata: Record<string, unknown> = {}) {
  return {
    id,
    type: 'file',
    title,
    content: '',
    metadata: {
      source: sourceLabel === 'Chat upload' ? 'chat_upload' : 'external_tool',
      externalToolFile: sourceLabel !== 'Chat upload',
      external_tool_file: sourceLabel !== 'Chat upload',
      tool_name: toolName,
      mimeType: 'text/markdown',
      mime_type: 'text/markdown',
      detectedMimeType: 'text/markdown',
      detected_mime_type: 'text/markdown',
      fileKind: 'markdown',
      file_kind: 'markdown',
      isText: true,
      is_text: true,
      previewAvailable: true,
      preview_available: true,
      sizeBytes: body.length,
      size_bytes: body.length,
      result_metadata: {
        family: sourceLabel.toLowerCase().replace(/\s+/g, '_'),
        product: sourceLabel,
        source_tool_name: toolName,
        native_viewer: 'file',
        renderer_strategy: 'artifact_type',
        renderer_key: 'file',
        file_capability: sourceLabel === 'Chat upload' ? 'attachment_read' : 'remote_file_download',
        external_tool_file: sourceLabel !== 'Chat upload',
        native_file_input: true,
        model_file_input: true,
        attachment_artifact_id: id,
        file_artifact_id: id,
      },
      tool_presentation: {
        family: sourceLabel.toLowerCase().replace(/\s+/g, '_'),
        result_kind: 'file',
        renderer_strategy: 'artifact_type',
        renderer_key: 'file',
        native_viewer: 'file',
        artifact_types: ['file'],
        file_capability: sourceLabel === 'Chat upload' ? 'attachment_read' : 'remote_file_download',
        produces_file_artifact: true,
        promotes_native_input: true,
      },
      ...extraMetadata,
    },
  };
}

const rawBodies: Record<string, string> = {
  'file-local': '# Local notes\n\nUploaded into the active chat.',
  'file-jira': '# Jira attachment\n\nSTREAM-4821 incident evidence.',
  'file-sharepoint': '# SharePoint roadmap\n\nGate 1 is complete.',
  'file-slack': '# Slack export\n\nPipeline canary is green.',
  'file-arxiv': '# arXiv paper\n\nSparse attention model notes.',
  'file-other-thread': '# Other thread\n\nThis file belongs to another chat.',
};

const artifacts = {
  'file-local': markdownArtifact('file-local', 'local-notes.md', 'Chat upload', 'thread_file_attachment', rawBodies['file-local']),
  'file-jira': markdownArtifact('file-jira', 'STREAM-4821-incident.md', 'Jira', 'jira_read_attachment', rawBodies['file-jira'], {
    managedLocalPath: 'atlassian/downloads/jira/STREAM-4821/incident.md',
    tool_args: { issueKey: 'STREAM-4821', attachmentId: '10032' },
  }),
  'file-sharepoint': markdownArtifact('file-sharepoint', 'Q3 Roadmap.md', 'SharePoint', 'sharepoint_read', rawBodies['file-sharepoint'], {
    managedLocalPath: 'sharepoint/downloads/q3-roadmap.md',
    tool_args: { drive_path: '/Shared Documents/Roadmaps/Q3 Roadmap.md' },
    source_url: 'https://contoso.sharepoint.com/sites/team/Shared%20Documents/Roadmaps/Q3%20Roadmap.md',
  }),
  'file-slack': markdownArtifact('file-slack', 'pipeline-export.md', 'Slack', 'slack_download_file', rawBodies['file-slack'], {
    managedLocalPath: 'slack/downloads/F123/pipeline-export.md',
    tool_args: { channel: 'C0STREAM', file_id: 'F123' },
    source_url: 'https://example.slack.com/files/F123/pipeline-export.md',
  }),
  'file-arxiv': markdownArtifact('file-arxiv', '2301.12345.md', 'arXiv', 'arxiv_download_paper', rawBodies['file-arxiv'], {
    managedLocalPath: 'arxiv/papers/2301.12345/paper.md',
    tool_args: { arxiv_id: '2301.12345' },
    source_url: 'https://arxiv.org/pdf/2301.12345',
  }),
  'file-other-thread': markdownArtifact('file-other-thread', 'Other Thread Export.md', 'Slack', 'slack_download_file', rawBodies['file-other-thread'], {
    managedLocalPath: 'slack/downloads/F999/other-thread-export.md',
    tool_args: { channel: 'C0OTHER', file_id: 'F999' },
  }),
};

function nowMinus(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

function recordFor(id: keyof typeof artifacts, threadId: string, threadTitle: string, sourceLabel: string, minutesAgo: number) {
  const artifact = artifacts[id];
  const metadata = artifact.metadata as Record<string, any>;
  return {
    id: artifact.id,
    artifact_id: artifact.id,
    thread_id: threadId,
    thread_title: threadTitle,
    type: 'file',
    title: artifact.title,
    source: metadata.source,
    source_label: sourceLabel,
    source_tool_name: metadata.tool_name,
    tool_name: metadata.tool_name,
    tool_call_id: `${artifact.id}-call`,
    file_kind: metadata.fileKind,
    mime_type: metadata.mimeType,
    byte_size: metadata.sizeBytes,
    content_storage: 'file',
    content_path: metadata.managedLocalPath || '',
    managed_local_path: metadata.managedLocalPath || '',
    preview_text: rawBodies[artifact.id].slice(0, 120),
    preview_available: true,
    preview_truncated: false,
    downloadable: true,
    cached: true,
    created_at: nowMinus(minutesAgo),
    family: metadata.result_metadata.family,
    product: metadata.result_metadata.product,
    remote_url: metadata.source_url,
    metadata,
  };
}

// The backend's connector-specific composer prompt for the remote reference —
// tests prove it reaches the production chat composer verbatim.
const REMOTE_DOWNLOAD_PROMPT = 'Call sharepoint_download_file with drive_path "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx" (item SPDOC-8842) to cache "Vendor SOW.docx" as a file artifact so it opens in Files.';

// A connector search surfaced this document but its bytes were never
// downloaded — the production wiring must present it as remote-only and route
// the ask-agent action through Settings → App → composer draft.
const remoteSowRecord = {
  id: 'remote-vendor-sow',
  artifact_id: 'remote-vendor-sow',
  thread_id: THREAD_ID,
  thread_title: 'Connector file review',
  type: 'file',
  title: 'Vendor SOW.docx',
  source: 'external_tool',
  source_label: 'SharePoint',
  source_tool_name: 'sharepoint_search',
  tool_name: 'sharepoint_search',
  tool_call_id: 'remote-vendor-sow-call',
  file_kind: 'docx',
  mime_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  byte_size: 48213,
  record_kind: 'remote_reference',
  availability: 'remote_only',
  cached: false,
  openable: false,
  can_request_download: true,
  removable: true,
  remote_id: 'SPDOC-8842',
  remote_url: 'https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx',
  remote_path: '/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx',
  request_tool_name: 'sharepoint_download_file',
  request_download_prompt: REMOTE_DOWNLOAD_PROMPT,
  preview_available: false,
  downloadable: false,
  created_at: nowMinus(2),
  metadata: { tool_args: { query: 'vendor SOW contracts' } },
};

const activeThreadRecords = [
  remoteSowRecord,
  recordFor('file-local', THREAD_ID, 'Connector file review', 'Chat upload', 4),
  recordFor('file-jira', THREAD_ID, 'Connector file review', 'Jira', 8),
  recordFor('file-sharepoint', THREAD_ID, 'Connector file review', 'SharePoint', 12),
  recordFor('file-slack', THREAD_ID, 'Connector file review', 'Slack', 16),
  recordFor('file-arxiv', THREAD_ID, 'Connector file review', 'arXiv', 20),
];
const allRecords = [
  ...activeThreadRecords,
  recordFor('file-other-thread', OTHER_THREAD_ID, 'Quarterly close scan', 'Slack', 24),
];

function jsonResponse(body: unknown, status = 200) {
  return {
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  };
}

function pagedFiles(files: unknown[], threadId: string | undefined, limit: number, offset: number) {
  const page = files.slice(offset, offset + limit);
  const nextOffset = offset + page.length < files.length ? offset + page.length : 0;
  return {
    files: page,
    scope: {
      thread_id: threadId || '',
      limit,
      offset,
      next_offset: nextOffset,
      returned: page.length,
      has_more: nextOffset > 0,
      truncated: nextOffset > 0,
      cached_file_limit_hit: false,
      remote_scan_limit: files.length,
      remote_scan_limit_hit: false,
      candidate_count: files.length,
    },
  };
}

async function installAppApi(page: Page) {
  // POSTs to the run endpoint would mean something was actually sent to the
  // agent — the ask-agent flow must only ever draft.
  const runPosts: string[] = [];
  const threads = [
    {
      id: THREAD_ID,
      title: 'Connector file review',
      created_at: Date.now() - 30_000,
      updated_at: Date.now() - 30_000,
    },
    {
      id: OTHER_THREAD_ID,
      title: 'Quarterly close scan',
      created_at: Date.now() - 60_000,
      updated_at: Date.now() - 60_000,
    },
  ];

  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const method = request.method().toUpperCase();
    const pathname = url.pathname;

    if (pathname === '/api/settings' && method === 'GET') {
      await route.fulfill(jsonResponse({ settings: {} }));
      return;
    }

    if (pathname === '/api/activation/status' && method === 'GET') {
      await route.fulfill(jsonResponse({ activated: true }));
      return;
    }

    if (/\/runs$/.test(pathname) && method === 'POST') {
      runPosts.push(pathname);
      await route.fulfill(jsonResponse({}));
      return;
    }

    if (pathname === '/api/secrets/status' && method === 'GET') {
      await route.fulfill(jsonResponse({ initialized: true, locked: false, configured: {} }));
      return;
    }

    if (pathname === '/api/threads' && method === 'GET') {
      await route.fulfill(jsonResponse({ threads }));
      return;
    }

    if (pathname === `/api/threads/${THREAD_ID}` && method === 'GET') {
      await route.fulfill(jsonResponse({ thread: threads[0], messages: [], artifacts: [] }));
      return;
    }

    if (pathname === `/api/threads/${OTHER_THREAD_ID}` && method === 'GET') {
      await route.fulfill(jsonResponse({ thread: threads[1], messages: [], artifacts: [] }));
      return;
    }

    if (pathname === '/api/files' && method === 'GET') {
      const limit = Number(url.searchParams.get('limit') || '150');
      const offset = Number(url.searchParams.get('offset') || '0');
      await route.fulfill(jsonResponse(pagedFiles(allRecords, undefined, limit, offset)));
      return;
    }

    if (pathname === `/api/threads/${THREAD_ID}/files` && method === 'GET') {
      const limit = Number(url.searchParams.get('limit') || '150');
      const offset = Number(url.searchParams.get('offset') || '0');
      await route.fulfill(jsonResponse(pagedFiles(activeThreadRecords, THREAD_ID, limit, offset)));
      return;
    }

    const rawMatch = pathname.match(/^\/api\/artifacts\/([^/]+)\/raw$/);
    if (rawMatch && method === 'GET') {
      const id = decodeURIComponent(rawMatch[1]);
      await route.fulfill({
        status: rawBodies[id] ? 200 : 404,
        body: rawBodies[id] || 'missing artifact',
        headers: {
          'content-type': rawBodies[id] ? 'text/markdown' : 'text/plain',
          'content-disposition': 'inline',
        },
      });
      return;
    }

    const artifactMatch = pathname.match(/^\/api\/artifacts\/([^/]+)$/);
    if (artifactMatch && method === 'GET') {
      const id = decodeURIComponent(artifactMatch[1]) as keyof typeof artifacts;
      const artifact = artifacts[id];
      await route.fulfill(artifact ? jsonResponse({ artifact }) : jsonResponse({ error: 'not found' }, 404));
      return;
    }

    await route.fulfill(jsonResponse({}));
  });

  return { runPosts };
}

async function openSettingsFilesTab(page: Page) {
  await page.getByRole('button', { name: 'Open settings' }).click();
  const settings = page.getByRole('dialog', { name: 'Settings' });
  await expect(settings).toBeVisible();
  await settings.getByRole('tab', { name: 'Files' }).click();
  const filesTab = settings.locator('[data-file-audit-tab]');
  await expect(filesTab).toBeVisible();
  return { settings, filesTab };
}

function rowFor(page: Page, title: string) {
  return page.locator('[data-file-audit-row]').filter({ hasText: title });
}

test('production Files view lists all files, filters to the active chat, and opens native viewer', async ({ page }) => {
  await installAppApi(page);

  await page.goto('/files', { waitUntil: 'domcontentloaded' });
  await expect(page.getByRole('heading', { name: 'All files' })).toBeVisible();

  for (const [title, source] of [
    ['local-notes.md', 'Chat upload'],
    ['STREAM-4821-incident.md', 'Jira'],
    ['Q3 Roadmap.md', 'SharePoint'],
    ['pipeline-export.md', 'Slack'],
    ['2301.12345.md', 'arXiv'],
    ['Other Thread Export.md', 'Slack'],
  ] as const) {
    const row = rowFor(page, title);
    await expect(row).toBeVisible();
    await expect(row.getByText(source, { exact: true })).toBeVisible();
  }

  await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('7 files');

  await page.getByRole('tab', { name: 'This Chat' }).click();
  await expect(page.getByRole('tab', { name: 'This Chat' })).toHaveAttribute('aria-selected', 'true');
  await expect(rowFor(page, 'Q3 Roadmap.md')).toBeVisible();
  await expect(rowFor(page, 'Other Thread Export.md')).toHaveCount(0);
  await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('6 files');

  await rowFor(page, 'Q3 Roadmap.md').getByRole('button', { name: 'Open Q3 Roadmap.md' }).click();
  const dialog = page.getByRole('dialog', { name: 'Artifact inspector' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('heading', { name: 'Q3 Roadmap.md' })).toBeVisible();
  await expect(dialog.getByText('SharePoint', { exact: true })).toBeVisible();
  await expect(dialog.getByText('from Connector file review')).toBeVisible();
  await expect(dialog.locator('[data-file-preview-kind="markdown-rendered"]')).toBeVisible();
  await expect(dialog.getByText('Gate 1 is complete.')).toBeVisible();
});

test('Settings Files tab shows the shared audit surface scoped to the active chat', async ({ page }) => {
  await installAppApi(page);
  await page.goto('/', { waitUntil: 'domcontentloaded' });

  const { settings, filesTab } = await openSettingsFilesTab(page);
  await expect(filesTab.locator('[data-file-audit-view]')).toBeVisible();

  // Production wiring defaults the tab to the active chat's scope: heading,
  // banner, and no cross-thread rows.
  await expect(filesTab.getByRole('heading', { name: 'This chat' })).toBeVisible();
  await expect(filesTab.locator('[data-scope-banner]')).toContainText('Showing files for Connector file review');
  await expect(rowFor(page, 'Other Thread Export.md')).toHaveCount(0);
  for (const [title, source] of [
    ['local-notes.md', 'Chat upload'],
    ['Q3 Roadmap.md', 'SharePoint'],
    ['Vendor SOW.docx', 'SharePoint'],
  ] as const) {
    const row = rowFor(page, title);
    await expect(row).toBeVisible();
    await expect(row.getByText(source, { exact: true })).toBeVisible();
  }
  await expect(filesTab.locator('[data-file-audit-total-badge]')).toHaveText('6 files');

  // All-files scope reaches records from other chats without leaving Settings.
  await settings.getByRole('tab', { name: 'All Files' }).click();
  await expect(rowFor(page, 'Other Thread Export.md')).toBeVisible();
  await expect(filesTab.locator('[data-file-audit-total-badge]')).toHaveText('7 files');

  // The full-view handoff closes Settings and lands on the /files page.
  await settings.getByRole('button', { name: 'Open full view' }).click();
  await expect(settings).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'All files' })).toBeVisible();
  await expect(page).toHaveURL(/\/files$/);
});

test('opening a file from Settings closes the dialog and opens the native viewer with provenance', async ({ page }) => {
  await installAppApi(page);
  await page.goto('/', { waitUntil: 'domcontentloaded' });

  const { settings } = await openSettingsFilesTab(page);
  await rowFor(page, 'Q3 Roadmap.md').getByRole('button', { name: 'Open Q3 Roadmap.md' }).click();

  // The viewer must not open behind the modal: Settings dismisses first.
  await expect(settings).toHaveCount(0);
  const dialog = page.getByRole('dialog', { name: 'Artifact inspector' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('heading', { name: 'Q3 Roadmap.md' })).toBeVisible();
  await expect(dialog.getByText('SharePoint', { exact: true })).toBeVisible();
  await expect(dialog.getByText('from Connector file review')).toBeVisible();
  await expect(dialog.locator('[data-file-preview-kind="markdown-rendered"]')).toBeVisible();
  await expect(dialog.getByText('Gate 1 is complete.')).toBeVisible();
});

test('asking from Settings drafts the download request in chat without sending it', async ({ page }) => {
  const { runPosts } = await installAppApi(page);
  await page.goto('/', { waitUntil: 'domcontentloaded' });

  const { settings } = await openSettingsFilesTab(page);
  const remoteRow = rowFor(page, 'Vendor SOW.docx');
  await expect(remoteRow).toHaveAttribute('data-file-status', 'remote');
  await expect(remoteRow.getByText('Remote only', { exact: true })).toBeVisible();

  await remoteRow.getByRole('button', { name: 'Ask agent to download Vendor SOW.docx' }).click();

  // Settings closes so the drafted prompt is immediately visible in the
  // composer — verbatim backend prompt, nothing sent.
  await expect(settings).toHaveCount(0);
  await expect(page.getByText('Added a download request for "Vendor SOW.docx" to the chat')).toBeVisible();
  await expect(page.locator('#chat-composer-input')).toHaveValue(new RegExp('Call sharepoint_download_file with drive_path'));
  await expect(page.locator('#chat-composer-input')).toHaveValue(new RegExp('Vendor SOW\\.docx'));
  expect(runPosts).toEqual([]);

  // Returning to Settings > Files shows the drafted-request memory on the row.
  const reopened = await openSettingsFilesTab(page);
  const requestedRow = reopened.filesTab.locator('[data-file-audit-row]').filter({ hasText: 'Vendor SOW.docx' });
  await expect(requestedRow).toHaveAttribute('data-file-request-state', 'requested');
  await expect(requestedRow.locator('[data-remote-status-note]'))
    .toContainText('You drafted a download request in Connector file review');
  expect(runPosts).toEqual([]);
});
