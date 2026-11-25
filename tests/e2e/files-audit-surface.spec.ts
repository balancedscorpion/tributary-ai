import { expect, test, type Locator, type Page } from '@playwright/test';

const ROUTE = '/dev/files-audit';

function rowFor(page: Page, title: string) {
  return page.locator('[data-file-audit-row]').filter({ hasText: title });
}

function sourceOverview(page: Page) {
  return page.getByRole('group', { name: 'Filter files by source' });
}

// Chip accessible names include the live count, e.g. "Jira (1 file)".
function sourceChip(page: Page, name: string) {
  return sourceOverview(page).getByRole('button', { name, exact: true });
}

async function openFileInspector(page: Page, title: string) {
  await rowFor(page, title).getByRole('button', { name: `Open ${title}` }).click();
  const dialog = page.getByRole('dialog', { name: 'Artifact inspector' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('heading', { name: title })).toBeVisible();
  return dialog;
}

async function closeFileInspector(page: Page) {
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: 'Artifact inspector' })).toHaveCount(0);
}

async function expectRawRead(page: Page, artifactId: string) {
  await expect.poll(async () => (
    await page.evaluate(() => ((window as any).__filesAuditFixtureRawReads || []) as string[])
  )).toContain(artifactId);
}

function detailValue(details: Locator, label: string) {
  return details
    .locator('dt')
    .filter({ hasText: label })
    .locator('xpath=following-sibling::dd[1]');
}

test.describe('files audit surface', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await expect(page.getByTestId('files-audit-fixture')).toBeVisible();
  });

  test('lists chat uploads and remote connector files with clear provenance', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'Files Audit Fixture' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Fixture files' })).toBeVisible();

    for (const [title, source] of [
      ['local-upload-notes.md', 'Chat upload'],
      ['STREAM-4821-incident.md', 'Jira'],
      ['Q3 Roadmap.md', 'SharePoint'],
      ['pipeline-export.md', 'Slack'],
      ['2301.12345.pdf', 'arXiv'],
      ['Vendor SOW.docx', 'SharePoint'],
      ['Legal Hold.pdf', 'SharePoint'],
    ] as const) {
      const row = rowFor(page, title);
      await expect(row).toBeVisible();
      await expect(row.getByText(source, { exact: true })).toBeVisible();
    }
  });

  test('loads additional file pages incrementally without duplicating rows', async ({ page }) => {
    await page.goto(`${ROUTE}?pageSize=3`, { waitUntil: 'domcontentloaded' });
    await expect(page.getByTestId('files-audit-fixture')).toBeVisible();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);
    await expect(rowFor(page, 'Vendor SOW.docx')).toBeVisible();
    await expect(rowFor(page, 'local-upload-notes.md')).toBeVisible();

    const status = page.locator('[data-file-audit-scope-status]');
    await expect(status).toBeVisible();
    await expect(status).toContainText('Showing 3 of 7 files.');
    // The headline badge must report loaded-of-available, not pretend the
    // loaded page is the whole audit.
    const totalBadge = page.locator('[data-file-audit-total-badge]');
    await expect(totalBadge).toHaveText('3 of 7 files');

    await status.getByRole('button', { name: 'Load more files' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(6);
    await expect(rowFor(page, 'STREAM-4821-incident.md')).toBeVisible();
    await expect(rowFor(page, 'pipeline-export.md')).toBeVisible();
    await expect(rowFor(page, 'Vendor SOW.docx')).toHaveCount(1);
    await expect(status).toContainText('Showing 6 of 7 files.');
    await expect(totalBadge).toHaveText('6 of 7 files');

    await status.getByRole('button', { name: 'Load more files' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);
    await expect(rowFor(page, '2301.12345.pdf')).toBeVisible();
    await expect(status).toHaveCount(0);
    await expect(totalBadge).toHaveText('7 files');
  });

  test('filters by source and searches preview text', async ({ page }) => {
    await sourceChip(page, 'SharePoint (3 files)').click();
    await expect(rowFor(page, 'Q3 Roadmap.md')).toBeVisible();
    await expect(rowFor(page, 'Vendor SOW.docx')).toBeVisible();
    await expect(rowFor(page, 'Legal Hold.pdf')).toBeVisible();
    await expect(rowFor(page, 'pipeline-export.md')).toHaveCount(0);
    await expect(rowFor(page, 'STREAM-4821-incident.md')).toHaveCount(0);

    await sourceChip(page, 'All (7 files)').click();
    await page.getByPlaceholder('Search files, source, chat, file type, or preview').fill('Canary green');
    await expect(rowFor(page, 'pipeline-export.md')).toBeVisible();
    await expect(rowFor(page, 'Q3 Roadmap.md')).toHaveCount(0);
  });

  test('uses native live previews instead of extracted text for previewable files', async ({ page }) => {
    const pdfRow = rowFor(page, '2301.12345.pdf');
    await expect(pdfRow).toBeVisible();
    await expect(pdfRow.getByText('Sparse Attention Models should still route through the native file viewer.')).toHaveCount(0);

    const dialog = await openFileInspector(page, '2301.12345.pdf');
    await expect(dialog.getByRole('toolbar', { name: 'PDF controls' })).toBeVisible();
    await expect(dialog.getByLabel('Scrollable PDF preview for 2301.12345.pdf')).toBeVisible();
    await expect(dialog.getByText('Sparse Attention Models should still route through the native file viewer.')).toHaveCount(0);
    await expectRawRead(page, 'file-arxiv-paper');
  });

  test('source overview lists every source bucket with counts and marks the active filter', async ({ page }) => {
    const overview = sourceOverview(page);
    await expect(overview).toBeVisible();
    for (const name of [
      'All (7 files)',
      'Chat upload (1 file)',
      'Jira (1 file)',
      'SharePoint (3 files)',
      'Slack (1 file)',
      'arXiv (1 file)',
    ]) {
      await expect(overview.getByRole('button', { name, exact: true })).toBeVisible();
    }

    const allChip = sourceChip(page, 'All (7 files)');
    const jiraChip = sourceChip(page, 'Jira (1 file)');
    await expect(allChip).toHaveAttribute('aria-pressed', 'true');
    await expect(jiraChip).toHaveAttribute('aria-pressed', 'false');

    await jiraChip.click();
    await expect(jiraChip).toHaveAttribute('aria-pressed', 'true');
    await expect(allChip).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(1);
    await expect(rowFor(page, 'STREAM-4821-incident.md')).toBeVisible();

    // Clicking the active chip toggles the filter back off.
    await jiraChip.click();
    await expect(allChip).toHaveAttribute('aria-pressed', 'true');
    await expect(jiraChip).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);
  });

  test('source overview counts follow the search query', async ({ page }) => {
    await page.getByPlaceholder('Search files, source, chat, file type, or preview').fill('Canary green');

    await expect(sourceChip(page, 'All (1 file)')).toBeVisible();
    await expect(sourceChip(page, 'Slack (1 file)')).toBeVisible();
    // Non-matching sources stay visible so the overview remains stable, with zeroed counts.
    await expect(sourceChip(page, 'Jira (0 files)')).toBeVisible();
    await expect(sourceChip(page, 'arXiv (0 files)')).toBeVisible();

    await sourceChip(page, 'Slack (1 file)').click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(1);
    await expect(rowFor(page, 'pipeline-export.md')).toBeVisible();
  });

  test('opens the origin chat from the file row', async ({ page }) => {
    const originButton = rowFor(page, 'Q3 Roadmap.md').getByRole('button', { name: 'Open origin chat for Q3 Roadmap.md' });
    await originButton.focus();
    await page.keyboard.press('Enter');
    await expect.poll(async () => (
      await page.evaluate(() => ((window as any).__filesAuditFixtureOriginOpens || []) as Array<{ threadId: string; toolCallId?: string }>)
    )).toContainEqual({ threadId: 'thread-files-fixture', toolCallId: 'file-sharepoint-plan-call' });
  });

  test('origin chat action expands the tool group and flashes the exact tool event', async ({ page }) => {
    await page.goto(`${ROUTE}?originTranscript=1`, { waitUntil: 'domcontentloaded' });
    await expect(page.getByTestId('files-audit-fixture')).toBeVisible();
    const transcript = page.getByTestId('files-audit-origin-transcript');
    const anchor = transcript.locator('[data-tool-call-anchor~="file-sharepoint-plan-call"]');

    // Completed turns mount minimized: only the pill, no card anchor yet.
    await expect(transcript.getByRole('button', { name: /Ran 6 tools/ })).toBeVisible();
    await expect(anchor).toHaveCount(0);

    const originButton = rowFor(page, 'Q3 Roadmap.md').getByRole('button', { name: 'Open origin chat for Q3 Roadmap.md' });
    await originButton.focus();
    await page.keyboard.press('Enter');

    // The owning group claims the reveal: expands, scrolls the card into
    // view, and applies the temporary highlight.
    await expect(anchor).toBeVisible();
    await expect(anchor).toHaveClass(/ta-reveal-flash/);
    await expect(anchor).toBeInViewport();
    await expect(transcript.getByText('sharepoint_read')).toBeVisible();

    // The flash is transient - the ring clears, the card stays.
    await expect(anchor).not.toHaveClass(/ta-reveal-flash/, { timeout: 5000 });
    await expect(anchor).toBeVisible();
  });

  test('opens a remote file through the native file viewer path', async ({ page }) => {
    const dialog = await openFileInspector(page, 'Q3 Roadmap.md');
    await expect(dialog.getByText('SharePoint', { exact: true })).toBeVisible();
    await expect(dialog.getByText('from Connector file review')).toBeVisible();
    await expect(dialog.getByText('sharepoint_read')).toBeVisible();
    await expect(dialog.locator('[data-file-preview-kind="markdown-rendered"]')).toBeVisible();
    await expect(dialog.getByText('Gate 1: finish migration readiness by July 15.')).toBeVisible();

    const detailsToggle = dialog.getByRole('button', { name: 'File details' });
    await expect(detailsToggle).toHaveAttribute('aria-expanded', 'false');
    await detailsToggle.click();
    await expect(detailsToggle).toHaveAttribute('aria-expanded', 'true');
    // Popover content portals outside the dialog element.
    const popover = page.locator('[data-file-provenance-details]');
    await expect(popover).toBeVisible();
    await expect(popover.getByText('file-sharepoint-plan-call')).toBeVisible();
    await expect(popover.getByText('sharepoint/downloads/q3-roadmap.md')).toBeVisible();
    await expect(popover.getByText('Remote source')).toBeVisible();
    await expect(detailValue(popover, 'Remote source').getByText('https://contoso.sharepoint.com/sites/team/Shared%20Documents/Roadmaps/Q3%20Roadmap.md', { exact: true })).toBeVisible();
    await expect(popover.getByText('/Shared Documents/Roadmaps/Q3 Roadmap.md', { exact: true })).toBeVisible();
    await expect(popover.getByText('Original downloadable · Removable: delete removes the app-managed copy')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(popover).toHaveCount(0);
    await expect(dialog).toBeVisible();

    await dialog.getByRole('button', { name: 'Origin chat' }).click();
    await expect.poll(async () => (
      await page.evaluate(() => ((window as any).__filesAuditFixtureOriginOpens || []) as Array<{ threadId: string; toolCallId?: string }>)
    )).toContainEqual({ threadId: 'thread-files-fixture', toolCallId: 'file-sharepoint-plan-call' });

    await expectRawRead(page, 'file-sharepoint-plan');
  });

  test('derives provenance for file artifacts opened directly from connector tool output', async ({ page }) => {
    await page.goto(`${ROUTE}?originTranscript=1`, { waitUntil: 'domcontentloaded' });
    await expect(page.getByTestId('files-audit-fixture')).toBeVisible();
    const transcript = page.getByTestId('files-audit-origin-transcript');
    await transcript.getByRole('button', { name: /Ran 6 tools/ }).click();

    for (const item of [
      {
        title: 'STREAM-4821-incident.md',
        openButton: 'Read text STREAM-4821-incident.md',
        callId: 'file-jira-attachment-call',
        source: 'Jira',
        tool: 'jira_read_attachment',
        type: 'text/markdown',
        request: 'issueKey:',
        remote: '10032',
        local: 'atlassian/downloads/jira/STREAM-4821/incident.md',
        previewKind: 'markdown',
      },
      {
        title: 'Q3 Roadmap.md',
        openButton: 'Read text Q3 Roadmap.md',
        callId: 'file-sharepoint-plan-call',
        source: 'SharePoint',
        tool: 'sharepoint_read',
        type: 'text/markdown',
        request: 'drive_path:',
        remote: 'https://contoso.sharepoint.com/sites/team/Shared%20Documents/Roadmaps/Q3%20Roadmap.md',
        local: 'sharepoint/downloads/q3-roadmap.md',
        previewKind: 'markdown',
      },
      {
        title: 'pipeline-export.md',
        openButton: 'Read text pipeline-export.md',
        callId: 'file-slack-export-call',
        source: 'Slack',
        tool: 'slack_download_file',
        type: 'text/markdown',
        request: 'file_id:',
        remote: 'https://example.slack.com/files/F123/pipeline-export.md',
        local: 'slack/downloads/F123/pipeline-export.md',
        previewKind: 'markdown',
      },
      {
        title: '2301.12345.pdf',
        openButton: 'View PDF 2301.12345.pdf',
        callId: 'file-arxiv-paper-call',
        source: 'arXiv',
        tool: 'arxiv_download_paper',
        type: 'application/pdf',
        request: 'arxiv_id:',
        remote: 'https://arxiv.org/pdf/2301.12345',
        local: 'arxiv/papers/2301.12345/paper.pdf',
        previewKind: 'pdf',
      },
      // MIME/kind live only in connector-native nested result_metadata
      // (Slack mimetype/filetype) and the title has no extension: the card
      // action and the native viewer must both route from nested metadata.
      {
        title: 'Quarterly Close Scan',
        openButton: 'View PDF Quarterly Close Scan',
        callId: 'file-slack-scan-call',
        source: 'Slack',
        tool: 'slack_download_file',
        type: 'application/pdf',
        request: 'file_id:',
        remote: 'https://files.slack.com/files-pri/T123-F999/download/quarterly-close-scan.pdf',
        local: 'slack/downloads/F999/quarterly-close-scan.pdf',
        previewKind: 'pdf',
      },
    ] as const) {
      const card = transcript.locator(`[data-tool-call-anchor~="${item.callId}"]`);
      await expect(card).toBeVisible();
      await transcript.getByRole('button', { name: item.openButton, exact: true }).first().click();

      const dialog = page.getByRole('dialog', { name: 'Artifact inspector' });
      await expect(dialog).toBeVisible();
      await expect(dialog.getByRole('heading', { name: item.title })).toBeVisible();
      if (item.previewKind === 'pdf') {
        await expect(dialog.getByLabel(`Scrollable PDF preview for ${item.title}`)).toBeVisible();
      } else {
        await expect(dialog.locator('[data-file-preview-kind="markdown-rendered"]')).toBeVisible();
      }
      await expect(dialog.getByText(item.source, { exact: true }).first()).toBeVisible();
      await expect(dialog.getByText(item.tool).first()).toBeVisible();
      await expect(dialog.getByText('from Connector file review')).toHaveCount(0);
      await expect(dialog.getByRole('button', { name: 'Origin chat' })).toHaveCount(0);

      await dialog.getByRole('button', { name: 'File details' }).click();
      const popover = page.locator('[data-file-provenance-details]');
      await expect(popover).toBeVisible();
      await expect(detailValue(popover, 'Source').getByText(item.source, { exact: true })).toBeVisible();
      await expect(detailValue(popover, 'Tool').getByText(item.tool)).toBeVisible();
      await expect(detailValue(popover, 'Type').getByText(item.type)).toBeVisible();
      await expect(detailValue(popover, 'Request').getByText(item.request)).toBeVisible();
      if (item.remote) {
        await expect(detailValue(popover, 'Remote source').getByText(item.remote, { exact: true })).toBeVisible();
      }
      if (item.local) {
        await expect(detailValue(popover, 'Local copy').getByText(item.local)).toBeVisible();
      }

      await page.keyboard.press('Escape');
      await expect(popover).toHaveCount(0);
      await closeFileInspector(page);
    }
  });

  test('cached connector details expose promoted remote locators', async ({ page }) => {
    const cases = [
      {
        title: 'STREAM-4821-incident.md',
        value: '10032',
      },
      {
        title: 'Q3 Roadmap.md',
        value: 'https://contoso.sharepoint.com/sites/team/Shared%20Documents/Roadmaps/Q3%20Roadmap.md',
      },
      {
        title: 'pipeline-export.md',
        value: 'https://example.slack.com/files/F123/pipeline-export.md',
      },
      {
        title: '2301.12345.pdf',
        value: 'https://arxiv.org/pdf/2301.12345',
      },
    ] as const;

    for (const item of cases) {
      const row = rowFor(page, item.title);
      await row.getByRole('button', { name: `Show details for ${item.title}` }).click();
      const details = row.locator('[data-file-provenance-details]');
      await expect(details.getByText('Remote source')).toBeVisible();
      await expect(detailValue(details, 'Remote source').getByText(item.value, { exact: true })).toBeVisible();
      await row.getByRole('button', { name: `Hide details for ${item.title}` }).click();
    }
  });

  test('opens every file source through the native file viewer', async ({ page }) => {
    for (const item of [
      {
        title: 'local-upload-notes.md',
        artifactId: 'file-chat-upload',
        source: 'Chat upload',
        tool: 'thread_file_attachment',
        text: 'The user uploaded this markdown file directly into the chat.',
      },
      {
        title: 'STREAM-4821-incident.md',
        artifactId: 'file-jira-attachment',
        source: 'Jira',
        tool: 'jira_read_attachment',
        text: 'Incident bundle for STREAM-4821.',
      },
      {
        title: 'pipeline-export.md',
        artifactId: 'file-slack-export',
        source: 'Slack',
        tool: 'slack_download_file',
        text: 'Canary green. Pipeline output came from a shared Slack file.',
      },
    ] as const) {
      const dialog = await openFileInspector(page, item.title);
      await expect(dialog.getByText(item.source, { exact: true })).toBeVisible();
      await expect(dialog.getByText(item.tool)).toBeVisible();
      await expect(dialog.locator('[data-file-preview-kind="markdown-rendered"]')).toBeVisible();
      await expect(dialog.getByText(item.text)).toBeVisible();
      await expectRawRead(page, item.artifactId);
      await closeFileInspector(page);
    }

    const arxivDialog = await openFileInspector(page, '2301.12345.pdf');
    await expect(arxivDialog.getByText('arXiv', { exact: true }).first()).toBeVisible();
    await expect(arxivDialog.getByText('arxiv_download_paper').first()).toBeVisible();
    await expect(arxivDialog.getByRole('toolbar', { name: 'PDF controls' })).toBeVisible();
    await expect(arxivDialog.getByLabel('Scrollable PDF preview for 2301.12345.pdf')).toBeVisible();
    await expectRawRead(page, 'file-arxiv-paper');
  });

  test('downloads raw bytes through the artifact raw endpoint', async ({ page }) => {
    const download = page.waitForEvent('download');
    await rowFor(page, 'pipeline-export.md').getByRole('button', { name: 'Download pipeline-export.md' }).click();
    expect((await download).suggestedFilename()).toBe('pipeline-export.md');

    await expect.poll(async () => (
      await page.evaluate(() => ((window as any).__filesAuditFixtureRawReads || []) as string[])
    )).toContain('file-slack-export');
  });

  test('row details disclosure exposes provenance with correct ARIA', async ({ page }) => {
    const row = rowFor(page, 'Q3 Roadmap.md');
    const toggle = row.getByRole('button', { name: 'Show details for Q3 Roadmap.md' });
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await toggle.click();

    const openToggle = row.getByRole('button', { name: 'Hide details for Q3 Roadmap.md' });
    await expect(openToggle).toHaveAttribute('aria-expanded', 'true');
    const controls = await openToggle.getAttribute('aria-controls');
    expect(controls).toBeTruthy();
    await expect(page.locator(`[id="${controls}"]`)).toBeVisible();
    await expect(openToggle).toBeFocused();

    const details = row.locator('[data-file-provenance-details]');
    await expect(details.getByText('sharepoint_read')).toBeVisible();
    await expect(details.getByText('drive_path:')).toBeVisible();
    await expect(details.getByText('/Shared Documents/Roadmaps/Q3 Roadmap.md', { exact: true })).toBeVisible();
    await expect(details.getByText('file-sharepoint-plan-call')).toBeVisible();
    await expect(details.getByText('sharepoint/downloads/q3-roadmap.md')).toBeVisible();
    await expect(details.getByText('Original downloadable · Removable: delete removes the app-managed copy')).toBeVisible();

    await openToggle.click();
    await expect(row.getByRole('button', { name: 'Show details for Q3 Roadmap.md' })).toHaveAttribute('aria-expanded', 'false');
    await expect(details).toHaveCount(0);
  });

  test('row details toggle is keyboard operable', async ({ page }) => {
    const row = rowFor(page, 'pipeline-export.md');
    await row.getByRole('button', { name: 'Show details for pipeline-export.md' }).focus();
    await page.keyboard.press('Enter');
    await expect(row.getByRole('button', { name: 'Hide details for pipeline-export.md' })).toHaveAttribute('aria-expanded', 'true');
    await page.keyboard.press('Space');
    await expect(row.getByRole('button', { name: 'Show details for pipeline-export.md' })).toHaveAttribute('aria-expanded', 'false');
  });

  test('request args redact secrets and skip duplicate paths', async ({ page }) => {
    const row = rowFor(page, 'pipeline-export.md');
    await row.getByRole('button', { name: 'Show details for pipeline-export.md' }).click();

    const details = row.locator('[data-file-provenance-details]');
    await expect(details.getByText('channel:')).toBeVisible();
    await expect(details.getByText('C0STREAM')).toBeVisible();
    await expect(details.getByText('file_id:')).toBeVisible();
    // Secret-shaped keys are dropped entirely.
    await expect(details.getByText('xoxb-secret-value')).toHaveCount(0);
    await expect(details.getByText('token:')).toHaveCount(0);
    // local_path duplicates the managed copy; the path appears once, in the Local copy row.
    await expect(details.getByText('slack/downloads/F123/pipeline-export.md')).toHaveCount(1);
  });

  test('copying a provenance value announces success politely', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    const row = rowFor(page, 'Q3 Roadmap.md');
    await row.getByRole('button', { name: 'Show details for Q3 Roadmap.md' }).click();
    await row.getByRole('button', { name: 'Copy local copy for Q3 Roadmap.md' }).click();

    await expect(page.getByText('Copied local copy')).toBeAttached();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('sharepoint/downloads/q3-roadmap.md');
  });

  test('removing a row while its details are expanded keeps focus in the list', async ({ page }) => {
    const row = rowFor(page, 'STREAM-4821-incident.md');
    await row.getByRole('button', { name: 'Show details for STREAM-4821-incident.md' }).click();
    await row.getByRole('button', { name: 'Remove STREAM-4821-incident.md' }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Remove file' }).click();

    await expect(rowFor(page, 'STREAM-4821-incident.md')).toHaveCount(0);
    // Focus lands on the next row's title button, not <body>.
    await expect(page.getByRole('button', { name: 'Open Q3 Roadmap.md' })).toBeFocused();
  });

  test('removes a file record and updates the visible list', async ({ page }) => {
    await rowFor(page, 'STREAM-4821-incident.md').getByRole('button', { name: 'Remove STREAM-4821-incident.md' }).click();
    // "Stored", not "cached" — the dialog title keeps the user-facing
    // vocabulary the header badge committed to.
    const dialog = page.getByRole('dialog', { name: 'Remove stored file?' });
    await expect(dialog).toContainText('delete the app-managed copy');
    await expect(dialog).toContainText('Source chat/tool messages will remain');
    await dialog.getByRole('button', { name: 'Remove file' }).click();

    await expect(rowFor(page, 'STREAM-4821-incident.md')).toHaveCount(0);
    await expect.poll(async () => (
      await page.evaluate(() => ((window as any).__filesAuditFixtureDeletes || []) as string[])
    )).toContain('file-jira-attachment');
  });

  test('remote-only rows show status and swap open/download for an ask-agent action', async ({ page }) => {
    const remoteRow = rowFor(page, 'Vendor SOW.docx');
    await expect(remoteRow).toHaveAttribute('data-file-status', 'remote');
    await expect(remoteRow.getByText('Remote only', { exact: true })).toBeVisible();
    // No cached bytes: no open affordance, no raw download.
    await expect(remoteRow.getByRole('button', { name: 'Open Vendor SOW.docx' })).toHaveCount(0);
    // exact: the ask-agent button's name contains "download Vendor SOW.docx".
    await expect(remoteRow.getByRole('button', { name: 'Download Vendor SOW.docx', exact: true })).toHaveCount(0);
    await expect(remoteRow.getByRole('button', { name: 'Remove remote reference for Vendor SOW.docx' })).toBeVisible();

    // Cached rows keep the openable title and stay unmarked.
    const cachedRow = rowFor(page, 'Q3 Roadmap.md');
    await expect(cachedRow).toHaveAttribute('data-file-status', 'cached');
    await expect(cachedRow.getByText('Remote only', { exact: true })).toHaveCount(0);
    await expect(cachedRow.getByRole('button', { name: 'Open Q3 Roadmap.md' })).toBeVisible();

    // Header summary surfaces the pending-download count, paired with the
    // user-facing opposite ("stored locally", not the internal "cached").
    await expect(page.getByText('2 remote only')).toBeVisible();
    await expect(page.getByText('5 stored locally')).toBeVisible();

    await remoteRow.getByRole('button', { name: 'Ask agent to download Vendor SOW.docx' }).click();
    await expect.poll(async () => (
      await page.evaluate(() => ((window as any).__filesAuditFixtureRemoteRequests || []) as Array<{ artifactId: string; title: string; prompt: string }>)
    )).toContainEqual({
      artifactId: 'file-sharepoint-remote',
      title: 'Vendor SOW.docx',
      // The exact request_download_prompt the fixture backend emitted — the
      // connector-specific instruction must survive normalization verbatim,
      // not be replaced by the generic frontend rebuild.
      prompt: 'Call sharepoint_download_file with drive_path "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx" (item SPDOC-8842) to cache "Vendor SOW.docx" as a file artifact so it opens in Files.',
    });
  });

  test('remote-only rows hide the ask-agent action when the backend forbids it', async ({ page }) => {
    // can_request_download: false — remote-only status still shows, but no
    // download CTA, no open affordance, and no raw download. The audit
    // reference itself can still be removed from Files/history.
    const blockedRow = rowFor(page, 'Legal Hold.pdf');
    await expect(blockedRow).toHaveAttribute('data-file-status', 'remote');
    await expect(blockedRow.getByText('Remote only', { exact: true })).toBeVisible();
    await expect(blockedRow.getByRole('button', { name: 'Ask agent to download Legal Hold.pdf' })).toHaveCount(0);
    await expect(blockedRow.getByRole('button', { name: 'Open Legal Hold.pdf' })).toHaveCount(0);
    await expect(blockedRow.getByRole('button', { name: 'Download Legal Hold.pdf', exact: true })).toHaveCount(0);
    await expect(blockedRow.getByRole('button', { name: 'Remove remote reference for Legal Hold.pdf' })).toBeVisible();
    await expect(blockedRow.getByRole('button', { name: 'Show details for Legal Hold.pdf' })).toBeVisible();
  });

  test('asking for a download marks the row as drafted without changing the action', async ({ page }) => {
    const remoteRow = rowFor(page, 'Vendor SOW.docx');
    const note = remoteRow.locator('[data-remote-status-note]');
    const askButton = remoteRow.getByRole('button', { name: 'Ask agent to download Vendor SOW.docx' });
    await expect(note).toHaveText('No local copy yet — the agent can download and cache it on request.');
    await expect(remoteRow).not.toHaveAttribute('data-file-request-state', 'requested');

    await askButton.click();

    // The note is the single "you already asked" carrier: it names the thread
    // the draft went to and claims only that a draft was created. The action
    // keeps its label — "Ask again" would assert the first draft still exists,
    // which the app cannot know.
    await expect(remoteRow).toHaveAttribute('data-file-request-state', 'requested');
    await expect(note).toContainText('You drafted a download request in Connector file review');
    await expect(note).toContainText('— send it there to download the file.');
    await expect(askButton).toBeVisible();

    // Re-asking stays possible (the user may have cleared the draft).
    await askButton.click();
    await expect.poll(async () => (
      await page.evaluate(() => (((window as any).__filesAuditFixtureRemoteRequests || []) as unknown[]).length)
    )).toBe(2);
    await expect(remoteRow).toHaveAttribute('data-file-request-state', 'requested');

    // The blocked remote row is untouched by the ledger.
    await expect(rowFor(page, 'Legal Hold.pdf')).not.toHaveAttribute('data-file-request-state', 'requested');
  });

  test('run-completed hint offers a refresh that reveals the downloaded file', async ({ page }) => {
    await page.goto(`${ROUTE}?downloadRequestFulfills=1`, { waitUntil: 'domcontentloaded' });
    await expect(page.getByTestId('files-audit-fixture')).toBeVisible();

    const row = rowFor(page, 'Vendor SOW.docx');
    await row.getByRole('button', { name: 'Ask agent to download Vendor SOW.docx' }).click();
    await expect(row).toHaveAttribute('data-file-request-state', 'requested');

    // The backend has cached the file (fulfills mode), but the mounted list
    // must not reload by itself — the row stays remote until the user acts.
    const hint = page.locator('[data-file-audit-run-hint]');
    await expect(hint).toHaveCount(0);
    await page.evaluate(() => (window as any).__filesAuditFixtureCompleteRun());
    await expect(hint).toBeVisible();
    await expect(hint).toContainText('files you asked it to download may be ready');
    await expect(row).toHaveAttribute('data-file-status', 'remote');

    await hint.getByRole('button', { name: 'Refresh files to check for requested downloads' }).click();
    await expect(hint).toHaveCount(0);
    // The hint's own button unmounted; focus stays in the surface.
    await expect(page.getByRole('textbox', { name: 'Search files' })).toBeFocused();

    // The refreshed list shows the record as stored locally: openable title,
    // no remote badge, no ask action, drafted-request note retired.
    await expect(row).toHaveAttribute('data-file-status', 'cached');
    await expect(row.getByRole('button', { name: 'Open Vendor SOW.docx' })).toBeVisible();
    await expect(row.getByText('Remote only', { exact: true })).toHaveCount(0);
    await expect(row.getByRole('button', { name: 'Ask agent to download Vendor SOW.docx' })).toHaveCount(0);
    await expect(row).not.toHaveAttribute('data-file-request-state', 'requested');
    await expect(page.getByText('6 stored locally')).toBeVisible();
    await expect(page.getByText('1 remote only')).toBeVisible();

    // The fulfilled request was cleared, so another run completion is silent.
    await page.evaluate(() => (window as any).__filesAuditFixtureCompleteRun());
    await page.waitForTimeout(150);
    await expect(hint).toHaveCount(0);
  });

  test('run-completed hint can be dismissed without refreshing', async ({ page }) => {
    const row = rowFor(page, 'Vendor SOW.docx');
    await row.getByRole('button', { name: 'Ask agent to download Vendor SOW.docx' }).click();
    await page.evaluate(() => (window as any).__filesAuditFixtureCompleteRun());
    const hint = page.locator('[data-file-audit-run-hint]');
    await expect(hint).toBeVisible();

    await hint.getByRole('button', { name: 'Dismiss download notice' }).click();
    await expect(hint).toHaveCount(0);
    await expect(page.getByRole('textbox', { name: 'Search files' })).toBeFocused();
    // Dismissal is presentation-only: the drafted-request memory and the
    // still-remote row are untouched.
    await expect(row).toHaveAttribute('data-file-request-state', 'requested');
    await expect(row).toHaveAttribute('data-file-status', 'remote');
  });

  test('remote-only filter chip isolates records awaiting download', async ({ page }) => {
    const chip = page.locator('[data-status-filter-chip="remote-only"]');
    await expect(chip).toHaveAttribute('aria-pressed', 'false');

    await chip.click();
    await expect(chip).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(2);
    await expect(rowFor(page, 'Vendor SOW.docx')).toBeVisible();
    await expect(rowFor(page, 'Legal Hold.pdf')).toBeVisible();

    await chip.click();
    await expect(chip).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);
  });

  test('remote-only details expose the remote source and access state', async ({ page }) => {
    const row = rowFor(page, 'Vendor SOW.docx');
    await row.getByRole('button', { name: 'Show details for Vendor SOW.docx' }).click();

    const details = row.locator('[data-file-provenance-details]');
    await expect(details.getByText('Remote source')).toBeVisible();
    await expect(details.getByText('https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx')).toBeVisible();
    await expect(details.getByText('sharepoint_search').first()).toBeVisible();
    await expect(details.getByText('Remote only: no local copy')).toBeVisible();
    // No local/stored copy rows exist for an uncached reference (exact:
    // substring matching would hit "no local copy" in the Access row).
    await expect(details.getByText('Local copy', { exact: true })).toHaveCount(0);
    // remote_path duplicates the URL's path segment here, so it must not
    // render a second locator row.
    await expect(details.getByText('Remote path', { exact: true })).toHaveCount(0);
  });

  test('path-only remote-only reference preserves, displays, and searches by remote_path', async ({ page }) => {
    // Legal Hold.pdf carries a server-relative remote_path and no web URL:
    // the path is the record's only locator and must surface in details.
    const row = rowFor(page, 'Legal Hold.pdf');
    await row.getByRole('button', { name: 'Show details for Legal Hold.pdf' }).click();

    const details = row.locator('[data-file-provenance-details]');
    await expect(details.getByText('Remote source')).toBeVisible();
    await expect(details.getByText('/sites/streaming/Shared Documents/Legal/Legal Hold.pdf')).toBeVisible();
    await expect(details.getByText('Remote only: no local copy')).toBeVisible();

    // The record is searchable by its server-relative path.
    await page.getByPlaceholder('Search files, source, chat, file type, or preview').fill('Shared Documents/Legal');
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(1);
    await expect(rowFor(page, 'Legal Hold.pdf')).toBeVisible();
  });

  test('removes a remote-only reference without implying source or chat deletion', async ({ page }) => {
    const remoteRow = rowFor(page, 'Vendor SOW.docx');
    await remoteRow.getByRole('button', { name: 'Remove remote reference for Vendor SOW.docx' }).click();

    const dialog = page.getByRole('dialog', { name: 'Remove remote reference?' });
    await expect(dialog).toContainText('from Files and file history only');
    await expect(dialog).toContainText('No local copy exists');
    await expect(dialog).toContainText('remote source is untouched');
    await expect(dialog).toContainText('source chat/tool messages will remain');
    await dialog.getByRole('button', { name: 'Remove reference' }).click();

    await expect(rowFor(page, 'Vendor SOW.docx')).toHaveCount(0);
    await expect(rowFor(page, 'Legal Hold.pdf')).toBeVisible();
    await expect.poll(async () => (
      await page.evaluate(() => ((window as any).__filesAuditFixtureDeletes || []) as string[])
    )).toContain('file-sharepoint-remote');
  });

  test('remote-only rows state in visible text whether the agent can download them', async ({ page }) => {
    // Downloadability is stated, not implied by button presence: a requestable
    // and a blocked reference must be distinguishable by reading the row.
    const requestable = rowFor(page, 'Vendor SOW.docx');
    await expect(requestable.locator('[data-remote-status-note]')).toHaveText(
      'No local copy yet — the agent can download and cache it on request.',
    );

    const blocked = rowFor(page, 'Legal Hold.pdf');
    await expect(blocked.locator('[data-remote-status-note]')).toHaveText(
      'No local copy — this connector can’t download the file automatically.',
    );

    // Cached rows carry no remote status note.
    await expect(rowFor(page, 'Q3 Roadmap.md').locator('[data-remote-status-note]')).toHaveCount(0);
  });

  test('remote-only access rows differentiate downloadability in details', async ({ page }) => {
    const row = rowFor(page, 'Vendor SOW.docx');
    await row.getByRole('button', { name: 'Show details for Vendor SOW.docx' }).click();
    await expect(row.locator('[data-file-provenance-details]').getByText('Agent can download on request')).toBeVisible();

    const blockedRow = rowFor(page, 'Legal Hold.pdf');
    await blockedRow.getByRole('button', { name: 'Show details for Legal Hold.pdf' }).click();
    await expect(blockedRow.locator('[data-file-provenance-details]').getByText('Automatic download unavailable')).toBeVisible();
  });

  test('search field is labeled by role and clearable', async ({ page }) => {
    const input = page.getByRole('textbox', { name: 'Search files' });
    await input.fill('Canary green');
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(1);

    await page.getByRole('button', { name: 'Clear search' }).click();
    await expect(input).toHaveValue('');
    // Clearing unmounts the X: focus must return to the input, not <body>.
    await expect(input).toBeFocused();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);
    // The clear affordance only renders while a query exists.
    await expect(page.getByRole('button', { name: 'Clear search' })).toHaveCount(0);
  });

  test('thread scope shows a banner with a direct path back to all files', async ({ page }) => {
    await page.getByRole('tab', { name: 'This Chat' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);

    const banner = page.locator('[data-scope-banner]');
    await expect(banner).toBeVisible();
    await expect(banner.getByText('Connector file review')).toBeVisible();

    await banner.getByRole('button', { name: 'View all files' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);
    await expect(banner).toHaveCount(0);
    await expect(page.getByRole('tab', { name: 'All Files' })).toHaveAttribute('aria-selected', 'true');
  });

  test('combined source and status filters announce together', async ({ page }) => {
    await sourceChip(page, 'SharePoint (3 files)').click();
    await page.locator('[data-status-filter-chip="remote-only"]').click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(2);
    // The (debounced) polite announcer composes both active filters; with
    // server-applied filters the loaded set is matches, not audit inventory.
    await expect(page.getByText('2 matches shown from SharePoint, remote only.')).toBeAttached();
  });

  test('truncated titles stay recoverable through native tooltips', async ({ page }) => {
    await expect(rowFor(page, 'Vendor SOW.docx').locator('[data-file-title-text]')).toHaveAttribute('title', 'Vendor SOW.docx');
    await expect(rowFor(page, 'Q3 Roadmap.md').locator('[data-file-title-button]')).toHaveAttribute(
      'title',
      'Open Q3 Roadmap.md in the file viewer',
    );
  });

  test('remote web URLs are clickable links while path-only locators stay copy-only', async ({ page }) => {
    // A remote-only file has no native-viewer path, so its web URL must be an
    // actual link — on the row and in details — not copy-only text.
    const remoteRow = rowFor(page, 'Vendor SOW.docx');
    const openRemote = remoteRow.getByRole('link', { name: 'Open Vendor SOW.docx at its remote source' });
    await expect(openRemote).toBeVisible();
    // The row link announces the new tab, matching the details-panel link.
    await expect(openRemote).toHaveAccessibleName(
      'Open Vendor SOW.docx at its remote source (opens in a new tab)',
    );
    await expect(openRemote).toHaveAttribute(
      'href',
      'https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx',
    );
    await expect(openRemote).toHaveAttribute('target', '_blank');

    await remoteRow.getByRole('button', { name: 'Show details for Vendor SOW.docx' }).click();
    const details = remoteRow.locator('[data-file-provenance-details]');
    const sourceLink = detailValue(details, 'Remote source').getByRole('link');
    await expect(sourceLink).toBeVisible();
    await expect(sourceLink).toHaveAttribute('target', '_blank');
    await expect(sourceLink).toHaveAttribute('rel', 'noopener noreferrer');

    // Server-relative paths and bare IDs are not browser-openable: no row
    // action, no details link — the copy button remains the access path.
    const blockedRow = rowFor(page, 'Legal Hold.pdf');
    await expect(blockedRow.locator('[data-file-open-remote]')).toHaveCount(0);
    await blockedRow.getByRole('button', { name: 'Show details for Legal Hold.pdf' }).click();
    const blockedDetails = blockedRow.locator('[data-file-provenance-details]');
    await expect(detailValue(blockedDetails, 'Remote source').getByRole('link')).toHaveCount(0);
  });

  test('load more moves keyboard focus to the first newly loaded row', async ({ page }) => {
    await page.goto(`${ROUTE}?pageSize=3`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);

    const loadMore = page.getByRole('button', { name: 'Load more files' });
    await loadMore.focus();
    await page.keyboard.press('Enter');

    await expect(page.locator('[data-file-audit-row]')).toHaveCount(6);
    // Focus lands on the first appended row's primary control instead of
    // dropping to <body> (the button is never disabled mid-load).
    await expect(
      rowFor(page, 'STREAM-4821-incident.md').getByRole('button', { name: 'Open STREAM-4821-incident.md' }),
    ).toBeFocused();
  });

  test('scope heading follows the active tab and the banner hands focus back', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'Fixture files' })).toBeVisible();

    await page.getByRole('tab', { name: 'This Chat' }).click();
    // The all-files title must not keep claiming the whole audit while the
    // list is scoped to one chat.
    await expect(page.getByRole('heading', { name: 'This chat' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Fixture files' })).toHaveCount(0);

    await page.locator('[data-scope-banner]').getByRole('button', { name: 'View all files' }).click();
    await expect(page.getByRole('heading', { name: 'Fixture files' })).toBeVisible();
    // The banner unmounts with the switch; focus moves to the search box.
    await expect(page.getByRole('textbox', { name: 'Search files' })).toBeFocused();
  });

  test('clipped request args expose their full value through a copy control', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    const fullValue = 'Title,Path,Author,Modified,ContentType,DocumentLink,SiteName,LibraryName,RetentionLabel,ComplianceTag';

    const row = rowFor(page, 'Legal Hold.pdf');
    await row.getByRole('button', { name: 'Show details for Legal Hold.pdf' }).click();
    const details = row.locator('[data-file-provenance-details]');
    await expect(details.getByText('select_fields:')).toBeVisible();
    // The display value is clipped at 80 chars, so the full value must stay
    // reachable without a mouse hover.
    await expect(details.getByText(fullValue, { exact: true })).toHaveCount(0);

    await row.getByRole('button', { name: 'Copy full select_fields request value for Legal Hold.pdf' }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(fullValue);
    await expect(page.getByText('Copied full select_fields request value')).toBeAttached();
  });

  test('zero-match search shows a recovery state instead of the empty-audit copy', async ({ page }) => {
    const search = page.getByPlaceholder('Search files, source, chat, file type, or preview');
    await search.fill('no-such-file-anywhere');
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(0);

    // The audit is not empty — the copy must blame the search, not claim
    // there are no files, and offer a one-click way back. The backend
    // searched before pagination, so the verdict covers the whole audit.
    const filteredEmpty = page.locator('[data-file-audit-filtered-empty]');
    await expect(filteredEmpty).toBeVisible();
    await expect(filteredEmpty).toContainText('No files match your search');
    await expect(filteredEmpty).toContainText('every file in this view was searched, not just loaded pages');
    await expect(page.getByText('No files seen yet')).toHaveCount(0);

    await filteredEmpty.getByRole('button', { name: 'Clear filters' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);
    await expect(search).toHaveValue('');
    // The clear button unmounts with the empty state; focus lands on search.
    await expect(search).toBeFocused();
  });

  test('clear filters also resets source and status filters', async ({ page }) => {
    await sourceChip(page, 'Jira (1 file)').click();
    await page.locator('[data-status-filter-chip="remote-only"]').click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(0);
    await page.locator('[data-file-audit-filtered-empty]').getByRole('button', { name: 'Clear filters' }).click();

    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);
    await expect(sourceChip(page, 'All (7 files)')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('[data-status-filter-chip="remote-only"]')).toHaveAttribute('aria-pressed', 'false');
  });

  test('removing the only filtered row lands focus on search, not body', async ({ page }) => {
    await sourceChip(page, 'Jira (1 file)').click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(1);

    await rowFor(page, 'STREAM-4821-incident.md').getByRole('button', { name: 'Remove STREAM-4821-incident.md' }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Remove file' }).click();

    // No rows survive: the list container unmounts in favour of the filtered
    // empty state, so focus must land on the always-mounted search box.
    await expect(page.locator('[data-file-audit-filtered-empty]')).toBeVisible();
    await expect(page.getByPlaceholder('Search files, source, chat, file type, or preview')).toBeFocused();
  });

  test('refresh keeps keyboard focus and reports its busy state', async ({ page }) => {
    await page.goto(`${ROUTE}?filesDelay=400`, { waitUntil: 'domcontentloaded' });
    await expect(page.getByTestId('files-audit-fixture')).toBeVisible();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);

    const refresh = page.getByRole('button', { name: 'Refresh files' });
    await refresh.focus();
    await page.keyboard.press('Enter');

    // Never disabled mid-load (that would blur focus to <body>); the wait is
    // reported through aria-busy instead.
    await expect(refresh).toHaveAttribute('aria-busy', 'true');
    await expect(refresh).toBeFocused();
    await expect(refresh).toHaveAttribute('aria-busy', 'false');
    await expect(refresh).toBeFocused();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(7);
  });

  test('row size and time cluster at the row edge with an absolute-date tooltip', async ({ page }) => {
    const time = rowFor(page, 'Q3 Roadmap.md').locator('[data-file-meta-time]');
    await expect(time).toBeVisible();
    // Relative text stays scannable; the exact timestamp rides on the title.
    await expect(time).toHaveAttribute('title', /\d/);
  });

  test('legacy backends count client-filtered matches grammatically in the status row', async ({ page }) => {
    // legacyFilters=1 simulates a backend without server-side source/status
    // filtering: chips fall back to client filtering over loaded records and
    // the status row pairs the loaded/available claim with the filtered count.
    await page.goto(`${ROUTE}?legacyFilters=1&pageSize=3`, { waitUntil: 'domcontentloaded' });
    await expect(page.getByTestId('files-audit-fixture')).toBeVisible();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);

    await page.locator('[data-status-filter-chip="remote-only"]').click();
    const status = page.locator('[data-file-audit-scope-status]');
    await expect(status).toContainText('Showing 3 of 7 files.');
    await expect(status).toContainText('2 files match the current filters.');

    await page.locator('[data-status-filter-chip="remote-only"]').click();
    await sourceChip(page, 'Chat upload (1 file)').click();
    await expect(status).toContainText('1 file matches the current filters.');
  });

  test('search queries the backend so matches on unloaded pages are found directly', async ({ page }) => {
    await page.goto(`${ROUTE}?pageSize=3`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);

    // The only match lives on the last unloaded page: server-side search
    // surfaces it directly instead of asking the user to page through.
    await page.getByPlaceholder('Search files, source, chat, file type, or preview').fill('2301');
    await expect(rowFor(page, '2301.12345.pdf')).toBeVisible();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(1);
    // No zero-match state and no coverage disclaimer: the whole audit was
    // searched, so there is nothing to widen into.
    await expect(page.locator('[data-file-audit-filtered-empty]')).toHaveCount(0);
    await expect(page.locator('[data-file-audit-search-coverage]')).toHaveCount(0);
    // The headline badge claims match counts while a search is applied.
    await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('1 match');
  });

  test('search pages through matches and resets pagination when the query changes', async ({ page }) => {
    await page.goto(`${ROUTE}?pageSize=2`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(2);

    const search = page.getByPlaceholder('Search files, source, chat, file type, or preview');
    await search.fill('sharepoint');
    const status = page.locator('[data-file-audit-scope-status]');
    // Counts describe matches, not the audit, while a search is applied.
    await expect(status).toContainText('Showing 2 of 3 matches.');
    await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('2 of 3 matches');
    await expect(rowFor(page, 'Vendor SOW.docx')).toBeVisible();
    await expect(rowFor(page, 'Legal Hold.pdf')).toBeVisible();

    // The pagination button agrees with the match nouns while narrowed.
    await status.getByRole('button', { name: 'Load more matches' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);
    await expect(rowFor(page, 'Q3 Roadmap.md')).toBeVisible();
    await expect(status).toHaveCount(0);
    await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('3 matches');

    // Clearing the query re-queries from offset 0 — back to the first page
    // of the unfiltered audit, not a stale concatenation.
    await page.getByRole('button', { name: 'Clear search' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(2);
    await expect(page.locator('[data-file-audit-scope-status]')).toContainText('Showing 2 of 7 files.');
  });

  test('server-side zero matches state that the whole audit was searched', async ({ page }) => {
    await page.goto(`${ROUTE}?pageSize=3`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);

    // Even with unloaded pages, zero backend matches is a verdict over the
    // whole audit — no widen button, no partial-coverage disclaimer.
    await page.getByPlaceholder('Search files, source, chat, file type, or preview').fill('no-such-file-anywhere');
    const filteredEmpty = page.locator('[data-file-audit-filtered-empty]');
    await expect(filteredEmpty).toBeVisible();
    await expect(filteredEmpty).toContainText('No files match your search');
    await expect(filteredEmpty).toContainText('every file in this view was searched, not just loaded pages');
    await expect(filteredEmpty.locator('[data-file-audit-search-coverage]')).toHaveCount(0);
    await expect(filteredEmpty.getByRole('button', { name: 'Load more files to widen the search' })).toHaveCount(0);

    await filteredEmpty.getByRole('button', { name: 'Clear filters' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);
    await expect(page.getByRole('textbox', { name: 'Search files' })).toBeFocused();
  });

  test('source chips are server-backed facets that span unloaded pages', async ({ page }) => {
    await page.goto(`${ROUTE}?pageSize=2`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(2);

    // Facet counts describe the whole audit, so every source bucket shows
    // with its real count even though only 2 of 7 rows are loaded — including
    // sources whose rows live entirely on unloaded pages.
    await expect(sourceChip(page, 'All (7 files)')).toBeVisible();
    await expect(sourceChip(page, 'arXiv (1 file)')).toBeVisible();
    await expect(sourceChip(page, 'SharePoint (3 files)')).toBeVisible();

    // Selecting a source re-queries the backend: the result set is every
    // SharePoint file in the audit, paginated — not a sieve over loaded rows.
    await sourceChip(page, 'SharePoint (3 files)').click();
    const status = page.locator('[data-file-audit-scope-status]');
    await expect(status).toContainText('Showing 2 of 3 matches.');
    await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('2 of 3 matches');
    await expect(rowFor(page, 'Vendor SOW.docx')).toBeVisible();
    await expect(rowFor(page, 'Legal Hold.pdf')).toBeVisible();
    // No partial-coverage disclaimers: the filter ran before pagination.
    await expect(page.locator('[data-file-audit-search-coverage]')).toHaveCount(0);

    await status.getByRole('button', { name: 'Load more matches' }).click();
    await expect(rowFor(page, 'Q3 Roadmap.md')).toBeVisible();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);
    await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('3 matches');
    // Other buckets keep their audit-wide counts while a source is selected.
    await expect(sourceChip(page, 'Jira (1 file)')).toBeVisible();
  });

  test('search and remote-only status combine server-side', async ({ page }) => {
    await page.goto(`${ROUTE}?pageSize=2`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(2);

    await page.getByPlaceholder('Search files, source, chat, file type, or preview').fill('pdf');
    await expect(rowFor(page, 'Legal Hold.pdf')).toBeVisible();
    await expect(rowFor(page, '2301.12345.pdf')).toBeVisible();

    // The status facet counts remote-only files among the search matches.
    const remoteChip = page.locator('[data-status-filter-chip="remote-only"]');
    await expect(remoteChip).toHaveAccessibleName('Remote only (1 file)');
    await remoteChip.click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(1);
    await expect(rowFor(page, 'Legal Hold.pdf')).toBeVisible();
    await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('1 match');
  });

  test('server-filtered zero matches state whole-audit coverage and keep the status chip reachable', async ({ page }) => {
    await page.goto(`${ROUTE}?pageSize=3`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);

    // The Jira bucket exists only on an unloaded page; selecting it fetches
    // the record from the server rather than filtering loaded rows.
    await sourceChip(page, 'Jira (1 file)').click();
    await expect(rowFor(page, 'STREAM-4821-incident.md')).toBeVisible();
    await expect(page.locator('[data-file-audit-total-badge]')).toHaveText('1 match');

    // Jira has no remote-only files: the facet reads 0, but the toggle stays
    // mounted (otherwise it could never be applied or cleared from here).
    const remoteChip = page.locator('[data-status-filter-chip="remote-only"]');
    await expect(remoteChip).toHaveAccessibleName('Remote only (0 files)');
    await remoteChip.click();

    // Zero results from server-applied filters is a whole-audit verdict:
    // no widen button, no partial-coverage note.
    const filteredEmpty = page.locator('[data-file-audit-filtered-empty]');
    await expect(filteredEmpty).toBeVisible();
    await expect(filteredEmpty).toContainText('No files match your filters');
    await expect(filteredEmpty).toContainText('they covered every file in this view, not just loaded pages');
    await expect(filteredEmpty.locator('[data-file-audit-search-coverage]')).toHaveCount(0);
    await expect(filteredEmpty.getByRole('button', { name: 'Load more files to widen the search' })).toHaveCount(0);

    await filteredEmpty.getByRole('button', { name: 'Clear filters' }).click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);
    await expect(page.getByRole('textbox', { name: 'Search files' })).toBeFocused();
  });

  test('legacy source and status zero-match discloses filter-only coverage and can widen inline', async ({ page }) => {
    await page.goto(`${ROUTE}?legacyFilters=1&pageSize=3`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(3);

    // Against a legacy backend the source/status filters stay client-side:
    // zero matches over loaded pages must blame filter coverage, not read as
    // a whole-audit verdict.
    await sourceChip(page, 'Chat upload (1 file)').click();
    await page.locator('[data-status-filter-chip="remote-only"]').click();
    const filteredEmpty = page.locator('[data-file-audit-filtered-empty]');
    await expect(filteredEmpty).toBeVisible();
    await expect(filteredEmpty.locator('[data-file-audit-search-coverage]'))
      .toHaveText('Filters only cover the 3 loaded files — 7 exist.');

    const widen = filteredEmpty.getByRole('button', { name: 'Load more files to widen the search' });
    await widen.click();
    // Page 2 still has no match: the empty state persists with updated
    // coverage and the still-mounted button keeps focus for the next page.
    await expect(filteredEmpty.locator('[data-file-audit-search-coverage]'))
      .toHaveText('Filters only cover the 6 loaded files — 7 exist.');
    await expect(widen).toBeFocused();

    await widen.click();
    // The final page still has no match: the widen button unmounts with
    // nothing left to load, so focus must land on Clear filters, not <body>.
    await expect(filteredEmpty).toContainText('All 7 loaded files are hidden');
    await expect(filteredEmpty.locator('[data-file-audit-search-coverage]')).toHaveCount(0);
    await expect(filteredEmpty.getByRole('button', { name: 'Load more files to widen the search' })).toHaveCount(0);
    await expect(filteredEmpty.getByRole('button', { name: 'Clear filters' })).toBeFocused();
  });

  test('legacy status filters over server search matches disclose match-only coverage', async ({ page }) => {
    // Mixed mode: the backend applies q server-side but ignores source/status
    // params, so the status filter falls back to client-side over loaded
    // search matches and the coverage copy must scope itself to those.
    await page.goto(`${ROUTE}?legacyFilters=1&pageSize=2`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(2);

    // Remote-only ON first (both loaded rows are remote), then search for
    // something whose first match page is entirely cached files.
    await page.locator('[data-status-filter-chip="remote-only"]').click();
    await expect(page.locator('[data-file-audit-row]')).toHaveCount(2);
    await page.getByPlaceholder('Search files, source, chat, file type, or preview').fill('markdown');

    // The search found matches, but the status filter hides all loaded ones:
    // the copy must scope the filter to loaded matches, not the whole search.
    const filteredEmpty = page.locator('[data-file-audit-filtered-empty]');
    await expect(filteredEmpty).toBeVisible();
    await expect(filteredEmpty).toContainText('All 2 loaded search matches are hidden by the source or status filters.');
    await expect(filteredEmpty.locator('[data-file-audit-search-coverage]'))
      .toHaveText('Source and status filters only cover the 2 loaded matches — 4 match the search.');

    await filteredEmpty.getByRole('button', { name: 'Load more files to widen the search' }).click();
    // All matches are now loaded and all are cached: nothing left to widen.
    await expect(filteredEmpty).toContainText('All 4 loaded search matches are hidden by the source or status filters.');
    await expect(filteredEmpty.locator('[data-file-audit-search-coverage]')).toHaveCount(0);
    await expect(filteredEmpty.getByRole('button', { name: 'Clear filters' })).toBeFocused();
  });
});
