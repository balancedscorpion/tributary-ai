import { expect, test, type Page, type Route } from '@playwright/test';

/**
 * Product-QA for connector-specific file artifacts through the native file
 * pipeline: ArtifactPanel -> registry (renderer_strategy 'artifact_type') ->
 * FileRenderer -> the expected native preview branch, with bytes and
 * extraction sidecars served from the same endpoints the app uses
 * (/api/artifacts/{id}/raw and /extracted).
 *
 * Complements files-audit-surface.spec.ts (markdown + PDF branches through
 * the Files UI) by covering the branches no connector fixture reaches there:
 * spreadsheet workbook previews, rendered slide decks, connector-native image
 * routing, full sandboxed HTML, and the no-preview binary fallback card.
 */

// Smallest valid PNG (1x1, transparent).
const PNG_BYTES = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
  'base64',
);

// Minimal but structurally valid single-page PDF so PdfCanvasViewer can
// actually parse and paint it (a stub body would exercise the error state,
// not the viewer).
function pdfBytes(): Buffer {
  const stream = 'BT /F1 14 Tf 36 96 Td (arXiv connector fixture PDF) Tj ET';
  const objects = [
    '1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n',
    '2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n',
    '3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>\nendobj\n',
    '4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n',
    `5 0 obj\n<< /Length ${stream.length} >>\nstream\n${stream}\nendstream\nendobj\n`,
  ];
  let body = '%PDF-1.4\n';
  const offsets: number[] = [];
  for (const object of objects) {
    offsets.push(body.length);
    body += object;
  }
  const xrefStart = body.length;
  body += `xref\n0 ${objects.length + 1}\n`;
  body += '0000000000 65535 f \n';
  for (const offset of offsets) {
    body += `${String(offset).padStart(10, '0')} 00000 n \n`;
  }
  body += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xrefStart}\n%%EOF\n`;
  return Buffer.from(body, 'latin1');
}

const HTML_HEADING = 'Release notes for 2.4';
const RAW_BODIES: Record<string, { body: Buffer | string; contentType: string }> = {
  'connector-file-slack-image': { body: PNG_BYTES, contentType: 'image/png' },
  'connector-file-arxiv-pdf': { body: pdfBytes(), contentType: 'application/pdf' },
  'connector-file-chat-html': {
    body: `<html><head><title>Release notes</title></head><body><h1>${HTML_HEADING}</h1></body></html>`,
    contentType: 'text/html',
  },
};

function sheetPreviewJson(id: string, name: string, lineItem: string, amount: number) {
  return {
    version: 1,
    sheet: { id, name, rowCount: 2, columnCount: 2, cellCount: 4 },
    metadata: {
      previewRowCount: 2,
      previewColumnCount: 2,
      totalRowCount: 2,
      totalColumnCount: 2,
      truncated: false,
    },
    columns: [
      { index: 1, name: 'A' },
      { index: 2, name: 'B' },
    ],
    headerRows: [1],
    rows: [
      {
        index: 1,
        cells: [
          { address: 'A1', row: 1, col: 1, display: 'Line item', header: true },
          { address: 'B1', row: 1, col: 2, display: 'Amount', header: true },
        ],
      },
      {
        index: 2,
        cells: [
          { address: 'A2', row: 2, col: 1, display: lineItem },
          { address: 'B2', row: 2, col: 2, display: amount, kind: 'number' },
        ],
      },
    ],
  };
}

const EXTRACTED_JSON: Record<string, unknown> = {
  'extracted/workbook.json': {
    version: 1,
    workbook: { sheetCount: 2, hiddenSheetCount: 0 },
    metadata: { totalCellCount: 8, formulaCount: 0 },
  },
  'extracted/worksheets/Revenue/preview.json': sheetPreviewJson('revenue', 'Revenue', 'Renewals expansion', 4200),
  'extracted/worksheets/Costs/preview.json': sheetPreviewJson('costs', 'Costs', 'Cloud infra spend', 1875),
};

const EXTRACTED_IMAGES = new Set(['slides/slide-1.png', 'slides/slide-2.png']);

interface ServedRequest {
  artifactId: string;
  kind: 'raw' | 'extracted';
  path?: string;
}

async function installArtifactRoutes(page: Page): Promise<ServedRequest[]> {
  const served: ServedRequest[] = [];

  await page.route('**/api/artifacts/*/raw*', async (route: Route) => {
    const url = new URL(route.request().url());
    const artifactId = decodeURIComponent(url.pathname.match(/^\/api\/artifacts\/([^/]+)\/raw$/)?.[1] || '');
    const raw = RAW_BODIES[artifactId];
    served.push({ artifactId, kind: 'raw' });
    if (!raw) {
      await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'unknown artifact' }) });
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

  await page.route('**/api/artifacts/*/extracted*', async (route: Route) => {
    const url = new URL(route.request().url());
    const artifactId = decodeURIComponent(url.pathname.match(/^\/api\/artifacts\/([^/]+)\/extracted$/)?.[1] || '');
    const path = url.searchParams.get('path') || '';
    served.push({ artifactId, kind: 'extracted', path });
    if (EXTRACTED_JSON[path] !== undefined) {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(EXTRACTED_JSON[path]),
      });
      return;
    }
    if (EXTRACTED_IMAGES.has(path)) {
      await route.fulfill({
        status: 200,
        body: PNG_BYTES,
        headers: { 'content-type': 'image/png' },
      });
      return;
    }
    await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'unknown path' }) });
  });

  return served;
}

test('connector file artifacts land in their native FileRenderer preview branches', async ({ page }) => {
  const browserErrors: string[] = [];
  page.on('pageerror', (error) => browserErrors.push(error.message));

  const served = await installArtifactRoutes(page);
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const cases = await page.evaluate(async () => {
    const harness = await import('/tests/e2e/fixtures/connector-file-renderer-harness.tsx' as string);
    return harness.mountConnectorFileRendererHarness();
  });

  expect(cases.map((tc) => tc.id)).toEqual([
    'sharepoint-xlsx',
    'jira-pptx',
    'slack-image',
    'arxiv-pdf',
    'chat-upload-html',
    'sharepoint-docx',
  ]);

  for (const tc of cases) {
    const section = page.getByTestId(`connector-file-case-${tc.id}`);
    await expect(section, `${tc.toolName} section should mount`).toBeVisible();
    // The panel header shows the artifact title: FileRenderer owns the panel,
    // not a connector inspector.
    await expect(section.getByText(tc.title).first()).toBeVisible();
  }

  // SharePoint xlsx -> JSON workbook preview with per-sheet tabs and grid.
  const xlsx = page.getByTestId('connector-file-case-sharepoint-xlsx');
  const sheetTabs = xlsx.getByRole('tablist', { name: 'Workbook sheets' });
  await expect(sheetTabs).toBeVisible();
  const revenueTab = sheetTabs.getByRole('tab', { name: /Revenue/ });
  const costsTab = sheetTabs.getByRole('tab', { name: /Costs/ });
  await expect(revenueTab).toHaveAttribute('aria-selected', 'true');
  await expect(costsTab).toHaveAttribute('aria-selected', 'false');
  await expect(xlsx.getByText('Renewals expansion')).toBeVisible();
  await costsTab.click();
  await expect(costsTab).toHaveAttribute('aria-selected', 'true');
  await expect(xlsx.getByText('Cloud infra spend')).toBeVisible();
  await expect(xlsx.getByText('Renewals expansion')).toHaveCount(0);

  // Jira pptx -> rendered slide deck with per-slide figures from /extracted.
  const pptx = page.getByTestId('connector-file-case-jira-pptx');
  await expect(pptx.getByText('Slide 1', { exact: true })).toBeVisible();
  await expect(pptx.getByText('Slide 2', { exact: true })).toBeVisible();
  const firstSlide = pptx.locator('img[alt="QBR Deck.pptx slide 1"]');
  await expect(firstSlide).toBeVisible();
  expect(await firstSlide.getAttribute('src')).toMatch(/^blob:/);

  // Slack image (nested connector-native MIME, extensionless title) ->
  // raw image branch rendered from fetched bytes as an object URL.
  const slackImage = page.getByTestId('connector-file-case-slack-image');
  const image = slackImage.locator('img[alt="deploy-graph"]');
  await expect(image).toBeVisible();
  expect(await image.getAttribute('src')).toMatch(/^blob:/);

  // arXiv pdf -> native PDF canvas viewer with its controls toolbar.
  const arxiv = page.getByTestId('connector-file-case-arxiv-pdf');
  await expect(arxiv.getByRole('toolbar', { name: 'PDF controls' })).toBeVisible();
  await expect(arxiv.getByLabel('Scrollable PDF preview for 2406.01234.pdf')).toBeVisible();

  // Chat-uploaded HTML -> full sandboxed srcDoc iframe (never a src URL).
  const html = page.getByTestId('connector-file-case-chat-upload-html');
  const htmlFrame = html.locator('iframe[data-file-preview-kind="html-full"]');
  await expect(htmlFrame).toBeVisible();
  expect(await htmlFrame.getAttribute('src')).toBeNull();
  expect(await htmlFrame.getAttribute('sandbox')).not.toBeNull();
  await expect(html.frameLocator('iframe[data-file-preview-kind="html-full"]').getByText(HTML_HEADING)).toBeVisible();

  // SharePoint docx with no preview -> metadata fallback card, not a text
  // branch and not a crash.
  const docx = page.getByTestId('connector-file-case-sharepoint-docx');
  await expect(docx.getByText('A reliable visual preview is not available for this file type', { exact: false })).toBeVisible();
  await expect(docx.locator('[data-file-preview-kind]')).toHaveCount(0);

  // No case fell through to a connector inspector: the three-zone shell's
  // Response heading must not appear inside any file case.
  for (const tc of cases) {
    const section = page.getByTestId(`connector-file-case-${tc.id}`);
    await expect(section.getByText('Response', { exact: true })).toHaveCount(0);
  }

  await expect(page.locator('[data-renderer-error]')).toHaveCount(0);
  expect(browserErrors).toEqual([]);

  // Byte transport went through the app endpoints, not inline content:
  // raw bytes for image/pdf/html, extraction sidecars for xlsx/pptx.
  const rawIds = new Set(served.filter((request) => request.kind === 'raw').map((request) => request.artifactId));
  expect(rawIds).toEqual(new Set([
    'connector-file-slack-image',
    'connector-file-arxiv-pdf',
    'connector-file-chat-html',
  ]));
  const extractedPaths = new Set(
    served.filter((request) => request.artifactId === 'connector-file-sharepoint-xlsx').map((request) => request.path),
  );
  expect(extractedPaths).toEqual(new Set([
    'extracted/workbook.json',
    'extracted/worksheets/Revenue/preview.json',
    'extracted/worksheets/Costs/preview.json',
  ]));
  const slidePaths = new Set(
    served.filter((request) => request.artifactId === 'connector-file-jira-pptx').map((request) => request.path),
  );
  expect(slidePaths).toEqual(new Set(['slides/slide-1.png', 'slides/slide-2.png']));
});
