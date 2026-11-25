import { expect, test, type Page } from '@playwright/test';

// Unified Sources dialog (/dev/sources fixture). Contract under test:
// - provenance-grouped list with reference-vs-copy captions;
// - add-source menu semantics (picker-backed folder reference, repo, copy);
// - deletion language split: "Stop referencing" (neutral) vs
//   "Delete imported copy" (destructive), with focus restoration;
// - GitHub repo browser: tree navigation, file opens through the native
//   viewer, unavailable-tree fallback offers the ask-agent path;
// - legacy composition vs unified /api/sources registry.

const ROUTE = '/dev/sources';

async function openDialog(page: Page) {
  await page.getByRole('button', { name: 'Manage sources' }).click();
  const dialog = page.getByRole('dialog', { name: 'Sources' });
  await expect(dialog).toBeVisible();
  return dialog;
}

function fixtureState(page: Page) {
  return page.evaluate(() => {
    const state = (window as any).__sourcesFixtureState;
    return {
      createdRules: state.createdRules,
      deletedRuleIds: state.deletedRuleIds,
      deletedProjectSources: state.deletedProjectSources,
      hiddenPatches: state.hiddenPatches,
      removedRepoIds: state.removedRepoIds,
      refreshedRepoIds: state.refreshedRepoIds,
      clonedUrls: state.clonedUrls,
      openFilesCalls: state.openFilesCalls,
      askAgentPrompts: state.askAgentPrompts,
      settingsSaves: state.settingsSaves,
    };
  });
}

test.describe('sources dialog (legacy composition)', () => {
  test('groups sources by provenance with honest semantics captions', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);

    await expect(dialog.getByRole('heading', { name: /This computer/ })).toBeVisible();
    await expect(dialog.getByRole('heading', { name: /GitHub repositories/ })).toBeVisible();
    await expect(dialog.getByRole('heading', { name: /Imported copies/ })).toBeVisible();

    const localRow = dialog.locator('[data-source-row]').filter({ hasText: 'Reports' }).first();
    await expect(localRow).toContainText('Read-only');
    await expect(localRow).toHaveAttribute('data-source-semantics', 'reference');

    const repoRow = dialog.locator('[data-source-row]').filter({ hasText: 'acme/analytics' });
    await expect(repoRow).toContainText('Local clone · reads without GitHub');
    await expect(repoRow).toContainText('main');

    const copyRow = dialog.locator('[data-source-row]').filter({ hasText: 'quarterly-uploads' });
    await expect(copyRow).toContainText('Imported snapshot — won’t update');
    await expect(copyRow).toContainText('42 files');
    await expect(copyRow).toHaveAttribute('data-source-semantics', 'copy');

    // The legacy clone is a copy: grouped under Imported copies, labeled as a
    // clone-time snapshot, never presented as a live reference.
    const legacyRepoRow = dialog.locator('[data-source-row]').filter({ hasText: 'legacy-repo' });
    await expect(legacyRepoRow).toContainText('Imported snapshot — won’t update');
    await expect(legacyRepoRow).toHaveAttribute('data-source-kind', 'project_snapshot');
    await expect(legacyRepoRow).toHaveAttribute('data-source-semantics', 'copy');
  });

  test('add-source menu states reference vs copy semantics before choosing', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    await dialog.getByRole('button', { name: 'Add a source' }).click();

    const menu = page.getByRole('menu');
    await expect(menu.getByRole('menuitem', { name: /Folder on this computer/ })).toContainText('Read-only reference — reads files in place');
    await expect(menu.getByRole('menuitem', { name: /GitHub repository/ })).toContainText('A local clone the assistant reads — pull to update');
    const importItem = menu.getByRole('menuitem', { name: /Import a copy/ });
    await expect(importItem).toContainText('Imported snapshot stored by the app — won’t update');
  });

  test('adding a folder uses the native picker and creates a grant', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    await dialog.getByRole('button', { name: 'Add a source' }).click();
    await page.getByRole('menuitem', { name: /Folder on this computer/ }).click();

    // No typed-path input anywhere: the row appears from the picker result.
    const newRow = dialog.locator('[data-source-row]').filter({ hasText: 'Case Files' });
    await expect(newRow).toBeVisible();
    await expect(newRow).toContainText('Read-only');

    const state = await fixtureState(page);
    expect(state.createdRules).toHaveLength(1);
    expect(state.createdRules[0].subject.local_root).toBe('/Users/fixture/Case Files');
    expect(state.createdRules[0].subject.local_kind).toBe('folder');
    // Settings stayed in sync with the grant.
    expect(state.settingsSaves.length).toBeGreaterThan(0);
    const lastSave = state.settingsSaves[state.settingsSaves.length - 1];
    expect(lastSave.localComputer.selectedFolders).toContain('/Users/fixture/Case Files');
  });

  test('adding a GitHub repository clones into the repo store and lists the new source', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    await dialog.getByRole('button', { name: 'Add a source' }).click();
    await page.getByRole('menuitem', { name: /GitHub repository/ }).click();

    const urlInput = dialog.getByLabel('Repository URL or owner/repo');
    await expect(urlInput).toBeFocused();
    await urlInput.fill('https://github.com/acme/widget');
    await dialog.getByRole('button', { name: 'Add repository' }).click();

    // The clone lands as a reference row in the GitHub group.
    const newRow = dialog.locator('[data-source-row]').filter({ hasText: 'acme/widget' });
    await expect(newRow).toBeVisible();
    await expect(newRow).toHaveAttribute('data-source-semantics', 'reference');
    const state = await fixtureState(page);
    expect(state.clonedUrls).toContain('https://github.com/acme/widget');
    // No legacy project-clone traffic: the repo store is the only clone path.
    expect(state.deletedProjectSources).toHaveLength(0);
  });

  test('an empty submit explains itself instead of dead-clicking', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    await dialog.getByRole('button', { name: 'Add a source' }).click();
    await page.getByRole('menuitem', { name: /GitHub repository/ }).click();

    const submit = dialog.getByRole('button', { name: 'Add repository' });
    await expect(submit).toHaveAttribute('aria-disabled', 'true');
    // aria-disabled keeps the control clickable for real users (unlike
    // `disabled`); force past Playwright's actionability heuristic.
    await submit.click({ force: true });
    // Focus routes back to the URL input; no pending row appears.
    await expect(dialog.getByLabel('Repository URL or owner/repo')).toBeFocused();
    await expect(dialog.locator('[data-source-pending-row]')).toHaveCount(0);
  });

  test('a cloned repository row offers Pull the latest changes', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    const repoRow = dialog.locator('[data-source-row]').filter({ hasText: 'acme/analytics' });
    await repoRow
      .getByRole('button', { name: 'Pull the latest changes into the local clone of acme/analytics' })
      .click();
    await expect
      .poll(async () => (await fixtureState(page)).refreshedRepoIds)
      .toContain('repo-analytics');
  });

  test('stop referencing is neutral, deletes the grant, and keeps focus in the dialog', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    await dialog.locator('[data-source-row]').filter({ hasText: 'Reports' })
      .getByRole('button', { name: 'Stop referencing Reports' }).click();

    const confirm = page.getByRole('dialog', { name: 'Stop referencing Reports?' });
    await expect(confirm).toContainText('Your files stay on your computer.');
    const confirmButton = confirm.getByRole('button', { name: 'Stop referencing' });
    // Reversible action: not styled as destructive.
    await expect(confirmButton).not.toHaveClass(/destructive/);
    await confirmButton.click();

    await expect(dialog.locator('[data-source-row]').filter({ hasText: 'Reports' })).toHaveCount(0);
    const state = await fixtureState(page);
    expect(state.deletedRuleIds).toContain('rule-reports');
    // Focus stays inside the dialog (never <body>).
    const focusInDialog = await page.evaluate(() => {
      const dialogEl = document.querySelector('[role="dialog"]');
      return Boolean(dialogEl && dialogEl.contains(document.activeElement));
    });
    expect(focusInDialog).toBe(true);
  });

  test('deleting an imported copy is explicit about snapshot semantics', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    await dialog.locator('[data-source-row]').filter({ hasText: 'quarterly-uploads' })
      .getByRole('button', { name: 'Delete the imported copy of quarterly-uploads' }).click();

    const confirm = page.getByRole('dialog', { name: 'Delete imported copy of quarterly-uploads?' });
    await expect(confirm).toContainText('Your original files are not affected.');
    await confirm.getByRole('button', { name: 'Delete copy' }).click();

    await expect(dialog.locator('[data-source-row]').filter({ hasText: 'quarterly-uploads' })).toHaveCount(0);
    const state = await fixtureState(page);
    expect(state.deletedProjectSources).toContain('quarterly-uploads');
  });

  test('repo browser drills in, walks the tree, and opens files natively', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    await dialog.getByRole('button', { name: 'Browse the files in acme/analytics' }).click();

    // Drill-in focus lands on Back (manual contract: same DialogContent).
    const backButton = dialog.getByRole('button', { name: 'Back to the sources list' });
    await expect(backButton).toBeFocused();

    const srcDir = dialog.getByRole('button', { name: 'src, folder' });
    await expect(srcDir).toHaveAttribute('aria-expanded', 'false');
    await srcDir.click();
    await expect(srcDir).toHaveAttribute('aria-expanded', 'true');
    await expect(dialog.getByRole('button', { name: 'main.go, file' })).toBeVisible();

    // Back restores focus to the originating Browse control.
    await backButton.click();
    await expect(dialog.getByRole('button', { name: 'Browse the files in acme/analytics' })).toBeFocused();

    // Open a file: dialog closes and the native viewer takes over.
    await dialog.getByRole('button', { name: 'Browse the files in acme/analytics' }).click();
    await dialog.getByRole('button', { name: 'README.md, file' }).click();
    await expect(page.getByRole('dialog', { name: 'Sources' })).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'README.md' })).toBeVisible();
    await expect(page.getByText('Fixture readme for the repo browser.')).toBeVisible();
  });

  test('unavailable tree falls back to an ask-agent path, not a dead end', async ({ page }) => {
    await page.goto(`${ROUTE}?treeUnavailable=1`);
    const dialog = await openDialog(page);
    await dialog.getByRole('button', { name: 'Browse the files in acme/analytics' }).click();

    await expect(dialog.getByText('File browsing needs the updated backend')).toBeVisible();
    await dialog.getByRole('button', { name: 'Ask the assistant to search this repository' }).click();
    await expect(page.getByRole('dialog', { name: 'Sources' })).toHaveCount(0);
    const state = await fixtureState(page);
    expect(state.askAgentPrompts[0]).toContain('acme/analytics');
  });

  test('without the repo store the dialog still lists references and copies', async ({ page }) => {
    await page.goto(`${ROUTE}?repoStore=0`);
    const dialog = await openDialog(page);
    await expect(dialog.locator('[data-source-row]').filter({ hasText: 'Reports' })).toBeVisible();
    await expect(dialog.locator('[data-source-row]').filter({ hasText: 'quarterly-uploads' })).toBeVisible();
    // No repo-store rows and no GitHub group heading (legacy clone stays a copy).
    await expect(dialog.getByRole('heading', { name: /GitHub repositories/ })).toHaveCount(0);
  });

  test('a clone attempt on a backend without the repo store degrades honestly, never to a snapshot', async ({ page }) => {
    await page.goto(`${ROUTE}?repoStore=0`);
    const dialog = await openDialog(page);
    await dialog.getByRole('button', { name: 'Add a source' }).click();
    await page.getByRole('menuitem', { name: /GitHub repository/ }).click();

    await dialog.getByLabel('Repository URL or owner/repo').fill('https://github.com/acme/widget');
    await dialog.getByRole('button', { name: 'Add repository' }).click();

    // The URL inputs are replaced by the explanation: there is no code path
    // that consumes the URL on this backend, and no silent snapshot fallback.
    const note = dialog.locator('[data-clone-unavailable]');
    await expect(note).toBeVisible();
    await expect(note).toContainText('can’t clone repositories');
    await expect(note).toContainText('Import a copy (upload folder)');
    await expect(note).toBeFocused();
    await expect(dialog.getByLabel('Repository URL or owner/repo')).toHaveCount(0);

    const state = await fixtureState(page);
    expect(state.clonedUrls).toHaveLength(0);
    expect(state.deletedProjectSources).toHaveLength(0);

    await dialog.getByRole('button', { name: 'Close the add-repository form' }).click();
    await expect(note).toHaveCount(0);
    await expect(dialog.getByRole('button', { name: 'Add a source' })).toBeFocused();
  });

  test('the empty state points at Add source instead of duplicating its actions', async ({ page }) => {
    await page.goto(`${ROUTE}?empty=1`);
    const dialog = await openDialog(page);
    await expect(dialog.getByText(/No sources yet — use/)).toBeVisible();
    // One real control: the existing Add source trigger, no duplicate buttons.
    await expect(dialog.getByRole('button', { name: 'Add a source' })).toBeVisible();
    await expect(dialog.locator('[data-source-row]')).toHaveCount(0);
  });

  test('a failing section reports partial data instead of blanking the dialog', async ({ page }) => {
    await page.goto(`${ROUTE}?failLocal=1`);
    const dialog = await openDialog(page);
    // Other sections still render...
    await expect(dialog.locator('[data-source-row]').filter({ hasText: 'acme/analytics' })).toBeVisible();
    // ...and the failure is stated with a retry.
    await expect(dialog.getByText(/Could not load local folder references|permissions store exploded/)).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Retry' })).toBeVisible();
  });

  test('footer routes file-level review to Files', async ({ page }) => {
    await page.goto(ROUTE);
    const dialog = await openDialog(page);
    await expect(dialog.getByText('Individual files from connectors and chat uploads live in Files.')).toBeVisible();
    await dialog.getByRole('button', { name: 'Open Files' }).click();
    await expect(page.getByRole('dialog', { name: 'Sources' })).toHaveCount(0);
    const state = await fixtureState(page);
    expect(state.openFilesCalls).toBe(1);
  });
});

test.describe('sources dialog (unified registry)', () => {
  test('renders /api/sources rows with the same grouping and semantics', async ({ page }) => {
    await page.goto(`${ROUTE}?backend=unified`);
    const dialog = await openDialog(page);

    const localRow = dialog.locator('[data-source-row]').filter({ hasText: 'Reports' });
    await expect(localRow).toHaveAttribute('data-source-kind', 'local_reference');

    // github_clone registry rows keep the app-managed clone kind.
    const repoRow = dialog.locator('[data-source-row]').filter({ hasText: 'acme/analytics' });
    await expect(repoRow).toHaveAttribute('data-source-kind', 'github_clone');
    await expect(repoRow).toContainText('Local clone · reads without GitHub');

    const copyRow = dialog.locator('[data-source-row]').filter({ hasText: 'quarterly-uploads' });
    await expect(copyRow).toHaveAttribute('data-source-semantics', 'copy');
    await expect(copyRow).toContainText('Imported snapshot — won’t update');
  });

  test('individual connector files are counted into the Files hand-off, not listed as rows', async ({ page }) => {
    await page.goto(`${ROUTE}?backend=unified`);
    const dialog = await openDialog(page);

    // The registry returns the connector file, but this dialog manages
    // collections; the file must route to Files instead of rendering a row.
    await expect(dialog.locator('[data-source-row]').filter({ hasText: 'quarterly-uploads' })).toBeVisible();
    await expect(dialog.locator('[data-source-row]').filter({ hasText: 'roadmap.pdf' })).toHaveCount(0);
    await expect(dialog.getByText(
      '1 individual file (from connectors and chat uploads) is in Files.',
    )).toBeVisible();
  });
});
