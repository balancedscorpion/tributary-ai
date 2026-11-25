import { expect, test } from '@playwright/test';

/**
 * DOM/behaviour coverage for the generated-image experience. The
 * /dev/image-generation route mounts the real CollapsedToolsSummary +
 * GeneratedImageCard + ArtifactPanel from synthetic generate_image tool calls,
 * result envelopes, and progress events, so every assertion exercises the
 * production renderers end to end (pure Vite, no backend).
 *
 * Each fixture entry renders its own card inside a
 * `[data-fixture-card="<label>"]` section (always visible) and has a matching
 * launch button (`button[data-fixture-label="<label>"]`) that opens the
 * ArtifactPanel inspector. Card assertions scope to the section; inspector
 * assertions click the launch button and scope to the dialog.
 */

const ROUTE = '/dev/image-generation';

function card(page: import('@playwright/test').Page, label: string) {
  return page.locator(`[data-fixture-card="${label}"]`);
}

function inspectorDialog(page: import('@playwright/test').Page) {
  return page.getByRole('dialog', { name: 'Artifact inspector' });
}

async function openInspector(page: import('@playwright/test').Page, label: string) {
  await page.locator(`button[data-fixture-label="${label}"]`).click();
}

/**
 * Completed / terminal tool groups mount minimized in CollapsedToolsSummary;
 * running groups (queued/generating/saving) are pinned open. Expand the group
 * if it is collapsed so the card is asserted through the real disclosure path.
 * When collapsed, the minimized pill is the only `aria-expanded="false"` button
 * in the section, so clicking it is unambiguous.
 */
async function expandGroup(page: import('@playwright/test').Page, label: string) {
  const root = card(page, label);
  const pill = root.locator('button[aria-expanded="false"]');
  if ((await pill.count()) > 0) await pill.first().click();
  return root;
}

const ALL_LABELS = [
  'image-queued',
  'image-generating',
  'image-saving',
  'image-completed',
  'image-completed-snake',
  'image-gallery',
  'image-moderation',
  'image-rate-limited',
  'image-provider-unavailable',
  'image-auth',
  'image-cancelled',
  'image-broken-preview',
];

test.describe('image-generation fixture', () => {
  test('fixture route mounts every card section + launch button', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await expect(page.getByRole('heading', { name: 'Image Generation' })).toBeVisible();
    for (const label of ALL_LABELS) {
      await expect(page.locator(`[data-fixture-card="${label}"]`)).toBeVisible();
      await expect(page.locator(`button[data-fixture-label="${label}"]`)).toBeVisible();
    }
    // Nothing is open until a launch button is clicked.
    await expect(inspectorDialog(page)).toHaveCount(0);
  });

  test('settings integration enables configured target and exposes explicit defaults', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    const settings = page.locator('[data-fixture-settings="image-generation"]');
    await expect(settings.getByText('Image generation', { exact: true })).toBeVisible();

    const toggle = settings.getByRole('switch', { name: 'Enable image generation' });
    await expect(toggle).not.toBeChecked();
    await toggle.click();
    await expect(toggle).toBeChecked();

    await expect(settings.getByText('Target configured — not verified. Generation is validated at first use.')).toBeVisible();
    await expect(settings.getByLabel('Image model')).toHaveValue('gpt-image-2');
    await expect(settings.getByLabel('Size')).toHaveValue('1536x1024');
    await expect(settings.getByLabel('Max images per run')).toHaveValue('1');
  });

  test('completed single image shows preview, metadata, and every action', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    const root = await expandGroup(page, 'image-completed');
    const cardEl = root.getByTestId('generated-image-card');
    await expect(cardEl).toHaveAttribute('data-state', 'completed');

    // Inline image with prompt-derived alt text (never an empty alt).
    await expect(root.getByRole('img', { name: /^Generated image: A neon city skyline/ })).toBeVisible();

    // Restrained metadata line: dimensions, format, quality, model.
    await expect(root.getByText(/1536×1024/)).toBeVisible();
    await expect(root.getByText(/Generated with gpt-image-2 via openai/)).toBeVisible();

    // Every documented action is reachable by accessible name.
    for (const name of ['Open image', 'Use in chat', 'Download image', 'Copy prompt', 'Edit prompt', 'Regenerate image', 'Image details']) {
      await expect(root.getByRole('button', { name }).first()).toBeVisible();
    }
  });

  test('snake_case metadata still resolves a completed card (tolerance)', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    const root = await expandGroup(page, 'image-completed-snake');
    await expect(root.getByTestId('generated-image-card')).toHaveAttribute('data-state', 'completed');
    await expect(root.getByRole('img', { name: /^Generated image: A watercolor fox/ })).toBeVisible();
    // WebP format + portrait dimensions parsed from snake_case metadata.
    await expect(root.getByText(/1024×1536/)).toBeVisible();
    await expect(root.getByText(/WEBP/)).toBeVisible();
  });

  test('queued and generating states reserve a stable frame and show stage text', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const queued = card(page, 'image-queued').getByTestId('generated-image-card');
    await expect(queued).toHaveAttribute('data-state', 'queued');
    await expect(card(page, 'image-queued').getByText('Preparing image')).toBeVisible();

    // The stage label is a visible span; an sr-only live region mirrors the
    // same text, so scope to the first (visible) match.
    const generating = card(page, 'image-generating').getByTestId('generated-image-card');
    await expect(generating).toHaveAttribute('data-state', 'generating');
    await expect(card(page, 'image-generating').getByText('Generating image').first()).toBeVisible();

    const saving = card(page, 'image-saving').getByTestId('generated-image-card');
    await expect(saving).toHaveAttribute('data-state', 'generating');
    await expect(card(page, 'image-saving').getByText('Saving image').first()).toBeVisible();
  });

  test('gallery renders four independent tiles', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    const root = await expandGroup(page, 'image-gallery');
    await expect(root.getByTestId('generated-image-gallery')).toBeVisible();
    await expect(root.getByTestId('generated-image-tile')).toHaveCount(4);
    // Each tile is independently openable.
    await expect(root.getByRole('button', { name: 'Open image' })).toHaveCount(4);
  });

  test('moderation block offers Edit prompt and never Retry', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    const root = await expandGroup(page, 'image-moderation');
    const cardEl = root.getByTestId('generated-image-card');
    await expect(cardEl).toHaveAttribute('data-state', 'moderation_blocked');
    await expect(root.getByText('Blocked by content policy')).toBeVisible();
    await expect(root.getByRole('button', { name: 'Edit prompt' })).toBeVisible();
    // Moderation is terminal: no retry affordance of any spelling.
    await expect(root.getByRole('button', { name: /retry/i })).toHaveCount(0);
  });

  test('transient failures classify with an error category', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const rate = (await expandGroup(page, 'image-rate-limited')).getByTestId('generated-image-card');
    await expect(rate).toHaveAttribute('data-state', 'failed');
    await expect(rate).toHaveAttribute('data-error-category', 'rate_limited');

    const provider = (await expandGroup(page, 'image-provider-unavailable')).getByTestId('generated-image-card');
    await expect(provider).toHaveAttribute('data-state', 'failed');
    await expect(provider).toHaveAttribute('data-error-category', 'provider_unavailable');
  });

  test('auth/target failure deep-links to image settings', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    const root = await expandGroup(page, 'image-auth');
    const cardEl = root.getByTestId('generated-image-card');
    await expect(cardEl).toHaveAttribute('data-state', 'failed');
    await expect(cardEl).toHaveAttribute('data-error-category', 'auth');
    await expect(root.getByText('Image target needs attention')).toBeVisible();
  });

  test('cancelled state is quiet — no alert', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    const root = await expandGroup(page, 'image-cancelled');
    await expect(root.getByTestId('generated-image-card')).toHaveAttribute('data-state', 'cancelled');
    await expect(root.getByText('Image generation cancelled').first()).toBeVisible();
    await expect(root.getByRole('alert')).toHaveCount(0);
  });

  test('broken preview falls back instead of showing a dead image', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    const root = await expandGroup(page, 'image-broken-preview');
    await expect(root.getByTestId('generated-image-broken-preview')).toBeVisible();
    await expect(root.getByText('Preview unavailable')).toBeVisible();
  });

  test('inspector opens with zoom controls and never dumps the base64 URI in Raw', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openInspector(page, 'image-completed');

    const dialog = inspectorDialog(page);
    await expect(dialog).toBeVisible();

    // Image canvas controls are present.
    await expect(dialog.getByRole('button', { name: 'Zoom in' })).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Zoom out' })).toBeVisible();

    // Rendered view: the data URI lives only in the <img src> attribute, not
    // in the dialog's visible text.
    await expect(dialog).not.toContainText('data:image');

    // Raw mode shows compact metadata JSON, not the multi-MB data URI.
    await dialog.getByRole('button', { name: 'Raw' }).click();
    await expect(dialog).not.toContainText('data:image');
    await expect(dialog).not.toContainText('base64');
    // The safe metadata view still identifies the image.
    await expect(dialog.getByText(/"generated"/)).toBeVisible();
  });

  test('generated image lights up the Files provenance block as "Generated"', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openInspector(page, 'image-completed');
    const dialog = inspectorDialog(page);
    await expect(dialog).toBeVisible();
    // Part A: fileProvenanceFromArtifact returns a "Generated" source for the
    // generated image, so the panel's provenance chip reads Generated.
    await expect(dialog.getByText('Generated', { exact: true }).first()).toBeVisible();
  });
});
