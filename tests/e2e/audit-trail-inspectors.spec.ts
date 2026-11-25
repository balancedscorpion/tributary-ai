import { expect, test } from '@playwright/test';

/**
 * Visual/DOM coverage for the audit-trail inspector family. The /dev/audit-trail
 * route mounts the real CollapsedToolsSummary + ArtifactPanel from synthetic
 * fixtures, so these assertions exercise the production renderers end to end.
 *
 * The inspector opens as a large modal overlay over a dimmed backdrop
 * (InspectorOverlay, role="dialog") — the same hosting the real app uses — so
 * every assertion is
 * scoped to the dialog. The tool name appears twice inside it (ArtifactPanel's
 * own <h2> chrome header + the inspector's header band), so the tool name is
 * matched via its heading role and the inspector itself is confirmed via its
 * unique three-zone labels + rendered data.
 */

const ROUTE = '/dev/audit-trail';

async function openFixture(page: import('@playwright/test').Page, label: string) {
  await page.locator(`button[data-fixture-label="${label}"]`).click();
}

function inspectorDialog(page: import('@playwright/test').Page) {
  return page.getByRole('dialog', { name: 'Artifact inspector' });
}

test.describe('audit-trail inspectors', () => {
  test('fixture route mounts the collapsed summary + launch buttons', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await expect(page.getByRole('heading', { name: 'Audit Trail Redesign' })).toBeVisible();
    for (const label of [
      'Repository list',
      'Pull request list',
      'Issue list',
      'Workflow runs',
      'SharePoint search',
      'SharePoint read',
      'Slack messages',
      'Jira issue',
      'Browser navigate',
      'Trino query',
      'Web search (generic)',
      'Provider hosted web search',
      'Provider hosted code interpreter',
    ]) {
      await expect(page.locator(`button[data-fixture-label="${label}"]`)).toBeVisible();
    }
    // Nothing is open until something is clicked — the overlay is not mounted.
    await expect(inspectorDialog(page)).toHaveCount(0);
  });

  test('inspector opens as a large modal overlay and closes with Escape', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openFixture(page, 'Slack messages');

    const dialog = inspectorDialog(page);
    await expect(dialog).toBeVisible();

    // Pop-up window, not a full-screen route: the dialog is inset from every
    // viewport edge (dimmed backdrop visible around it) while still being
    // large — most of the viewport in both dimensions.
    const box = await dialog.boundingBox();
    const viewport = page.viewportSize();
    if (!box || !viewport) throw new Error('missing dialog box or viewport');
    expect(box.x).toBeGreaterThan(0);
    expect(box.y).toBeGreaterThan(0);
    expect(box.width).toBeLessThan(viewport.width);
    expect(box.height).toBeLessThan(viewport.height);
    expect(box.width).toBeGreaterThan(viewport.width * 0.7);
    expect(box.height).toBeGreaterThan(viewport.height * 0.7);

    // Close affordances: Escape...
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);

    // ...a click on the dimmed backdrop...
    await openFixture(page, 'Slack messages');
    await expect(dialog).toBeVisible();
    await page.getByTestId('inspector-backdrop').click({ position: { x: 5, y: 5 } });
    await expect(dialog).toHaveCount(0);

    // ...and the panel's own close button.
    await openFixture(page, 'Slack messages');
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Close inspector' }).click();
    await expect(dialog).toHaveCount(0);
  });

  test('generic connector inspector renders structured data, not raw JSON', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openFixture(page, 'Slack messages');
    const inspector = inspectorDialog(page);

    // Opened the right tool, and the three-zone shell is present.
    await expect(inspector.getByRole('heading', { name: 'slack_get_messages' })).toBeVisible();
    await expect(inspector.getByText('Request', { exact: true })).toBeVisible();
    await expect(inspector.getByText('Response', { exact: true })).toBeVisible();
    await expect(inspector.getByText('Metadata', { exact: true })).toBeVisible();

    // The messages array rendered as a real table with the actual content —
    // proving we did not fall back to a raw JSON blob.
    await expect(inspector.locator('table')).toBeVisible();
    await expect(inspector.getByText('Canary green')).toBeVisible();
  });

  test('connector-less generic tool still gets the inspector family', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openFixture(page, 'Web search (generic)');
    const inspector = inspectorDialog(page);

    await expect(inspector.getByRole('heading', { name: 'web_search' })).toBeVisible();
    await expect(inspector.getByText('Response', { exact: true })).toBeVisible();
    await expect(inspector.getByText('Streaming ingestion patterns')).toBeVisible();
  });

  test('provider-hosted tools show hosted identity and payloads in the inspector', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    await openFixture(page, 'Provider hosted web search');
    let inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'web_search' })).toBeVisible();
    await expect(inspector.getByText('Anthropic', { exact: true }).first()).toBeVisible();
    await expect(inspector.getByText('anthropic-messages/web_search')).toBeVisible();
    await expect(inspector.getByText('Hosted search audit sentinel')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).toHaveCount(0);

    await openFixture(page, 'Provider hosted code interpreter');
    inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'code_interpreter' })).toBeVisible();
    await expect(inspector.getByText('OpenAI Responses', { exact: true }).first()).toBeVisible();
    await expect(inspector.getByText('openai-responses/code_interpreter')).toBeVisible();
    await expect(inspector.getByText('hosted code audit sentinel', { exact: true }).first()).toBeVisible();
    await expect(inspector.getByText('resp-container-audit-1')).toBeVisible();
  });

  test('GitHub read widgets render backend-shaped outputs through the native family renderer', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    await openFixture(page, 'Repository list');
    let inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'github_list_repositories' })).toBeVisible();
    await expect(inspector.getByText('GitHub', { exact: true }).first()).toBeVisible();
    await expect(inspector.getByText('Response · 3 repositories')).toBeVisible();
    await expect(inspector.locator('table')).toBeVisible();
    await expect(inspector.locator('td').getByText('example-org/issue-tracker', { exact: true })).toBeVisible();
    await expect(inspector.getByText('source/audit-ui-fixtures')).toBeVisible();
    await expect(inspector.getByText('git@github.com:example-org/audit-ui-fixtures.git')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).toHaveCount(0);

    await openFixture(page, 'Pull request list');
    inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'github_list_pull_requests' })).toBeVisible();
    await expect(inspector.getByText('Response · 3 pull requests')).toBeVisible();
    await expect(inspector.getByText('Stream rate-limit envelope to the client and reuse it in toast components')).toBeVisible();
    await expect(inspector.getByText('stream-rate-limit')).toBeVisible();
    await expect(inspector.getByText('review required')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).toHaveCount(0);

    await openFixture(page, 'Issue list');
    inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'github_list_issues' })).toBeVisible();
    await expect(inspector.getByText('Response · 2 issues')).toBeVisible();
    await expect(inspector.getByText("Audit-trail group rolls up errors but doesn't expose the first failing tool")).toBeVisible();
    await expect(inspector.getByText('truncated at 2')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).toHaveCount(0);

    await openFixture(page, 'Workflow runs');
    inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'github_list_workflow_runs' })).toBeVisible();
    await expect(inspector.getByText('Response · 3 runs')).toBeVisible();
    await expect(inspector.getByText('Fix audit-trail tool scope leaking across chats')).toBeVisible();
    await expect(inspector.getByText('desktop-bridge-split')).toBeVisible();
    await expect(inspector.getByText('Add per-chat tool audit UI')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).toHaveCount(0);

    await openFixture(page, 'Workflow run jobs');
    inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'github_get_workflow_run_jobs' })).toBeVisible();
    await expect(inspector.getByText('2 jobs')).toBeVisible();
    await expect(inspector.getByRole('button', { name: /lint/i })).toBeVisible();
    const failingJob = inspector.getByRole('button', { name: /test/i });
    await expect(failingJob).toBeVisible();
    await failingJob.click();
    await expect(inspector.getByText('Vitest')).toBeVisible();
  });

  test('GitHub approval and gh command widgets preserve executable context', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    await openFixture(page, 'Approval (clone)');
    let inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'github_clone_repository' })).toBeVisible();
    await expect(inspector.getByText('Clone a GitHub repository example-org/model-eval')).toBeVisible();
    await expect(inspector.getByText('Changes or sends data outside this app')).toBeVisible();
    await expect(inspector.getByText('$ gh repo clone example-org/model-eval -- --depth 1 --branch main')).toBeVisible();
    await expect(inspector.getByRole('button', { name: 'Allow once' })).toBeVisible();
    await expect(inspector.getByRole('button', { name: "Don’t allow" })).toBeVisible();
    await expect(inspector.getByText(/Waiting for your decision/)).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).toHaveCount(0);

    await openFixture(page, 'gh command');
    inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'github_run_gh_command' })).toBeVisible();
    await expect(inspector.getByText('gh pr list --repo example-org/issue-tracker --state open --limit 3')).toBeVisible();
    await expect(inspector.getByText('stdout')).toBeVisible();
    await expect(inspector.getByText('#412  Stream rate-limit envelope to the client and reuse it in toast components  stream-rate-limit')).toBeVisible();
    await expect(inspector.getByText('exit 0')).toBeVisible();
  });

  test('Jira response widgets render semantic content inside the generic inspector', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    await openFixture(page, 'Jira search');
    let inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'jira_search_issues' })).toBeVisible();
    await expect(inspector.getByText('Jira JQL search')).toBeVisible();
    await expect(inspector.getByText('GET /rest/api/3/search/jql')).toBeVisible();
    await expect(
      inspector
        .getByRole('code')
        .filter({ hasText: 'project = STREAM AND statusCategory != Done ORDER BY priority DESC' }),
    ).toBeVisible();
    await expect(inspector.getByText('4 of 28 issues')).toBeVisible();
    await expect(inspector.getByRole('link', { name: 'STREAM-4821' })).toBeVisible();
    await expect(inspector.getByText('Backpressure stalls ingestion when Kafka lag > 5m')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).toHaveCount(0);

    await openFixture(page, 'Jira links (partial)');
    inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'jira_add_related_tickets_execute' })).toBeVisible();
    await expect(inspector.getByText('1 of 2 links created on STREAM-4821')).toBeVisible();
    await expect(inspector.getByText('STREAM-4633')).toBeVisible();
    await expect(inspector.getByText('STREAM-9999', { exact: true })).toBeVisible();
    await expect(inspector.getByText('Issue STREAM-9999 does not exist or you do not have permission to see it.')).toBeVisible();
  });

  test('SharePoint search and file reads use structured connector and native file views', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    await openFixture(page, 'SharePoint search');
    let inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'sharepoint_search' })).toBeVisible();
    await expect(inspector.getByText('SharePoint', { exact: true }).first()).toBeVisible();
    await expect(inspector.getByText('Response', { exact: true })).toBeVisible();
    await expect(inspector.locator('table')).toBeVisible();
    await expect(inspector.getByText('Q3 Roadmap.md', { exact: true })).toBeVisible();
    await expect(inspector.getByText('Launch sequencing, adoption targets, and shared-risk milestones for Q3.')).toBeVisible();
    await expect(inspector.getByText('/sites/Product/Shared Documents/Q3 Roadmap.md')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).toHaveCount(0);

    await openFixture(page, 'SharePoint read');
    inspector = inspectorDialog(page);
    await expect(inspector.getByRole('heading', { name: 'Q3 Roadmap.md' })).toBeVisible();
    await expect(inspector.getByText('File', { exact: true }).first()).toBeVisible();
    await expect(inspector.getByText('text/markdown')).toBeVisible();
    await expect(inspector.getByRole('heading', { name: 'Q3 Roadmap', exact: true })).toBeVisible();
    await expect(inspector.getByText('Launch sequencing is split into three confidence gates.')).toBeVisible();
    await expect(inspector.getByText('Gate 1: finish migration readiness by July 15')).toBeVisible();
    await expect(inspector.getByText('Shared risk: enterprise rollout depends on the approvals queue audit.')).toBeVisible();
  });

  test('connector errors keep the diagnostic skeleton', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openFixture(page, 'Slack error (scope)');
    const inspector = inspectorDialog(page);

    await expect(inspector.getByText('Missing OAuth scope')).toBeVisible();
    await expect(inspector.getByText('Try this', { exact: true })).toBeVisible();
    await expect(inspector.getByText('approve the chat:write scope')).toBeVisible();
  });

  test('write tools surface the shared approval card', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openFixture(page, 'Jira create (approval)');
    const inspector = inspectorDialog(page);

    await expect(inspector.getByRole('group', { name: /Approval needed/i })).toBeVisible();
    await expect(inspector.getByText('Create issue in STREAM')).toBeVisible();
    await expect(inspector.getByText('Waiting for your decision')).toBeVisible();
  });

  test('clicking a tool card body opens the audit overlay', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    // The collapsed summary's generic card body is the primary open affordance.
    const cardBody = page
      .getByRole('button', { name: /Open full audit — slack_get_messages/ })
      .first();
    await expect(cardBody).toBeVisible();
    await cardBody.click();

    const inspector = inspectorDialog(page);
    await expect(inspector).toBeVisible();
    await expect(inspector.getByRole('heading', { name: 'slack_get_messages' })).toBeVisible();
  });

  test('arXiv summary distinguishes starvation, saturation, and screening', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const arxivGroup = page.getByRole('button', { name: /Searched arXiv/ }).first();
    await expect(arxivGroup).toContainText('4 queries');
    await expect(arxivGroup).toContainText('5 returned');
    await expect(arxivGroup).toContainText('3 unique');
    await expect(arxivGroup).toContainText('2 agent-kept');

    await arxivGroup.click();
    await expect(page.getByText('2 queries found nothing')).toBeVisible();
    await expect(page.getByText('0 results — starvation, not convergence')).toBeVisible();
    await expect(page.getByText('Saturated')).toBeVisible();
    await expect(page.getByText('Screened 3 candidates -> 2 kept · 1 possible · 0 excluded')).toBeVisible();
    await expect(page.getByText(/observed arXiv span/)).toBeVisible();
    await expect(page.getByText(/summed measured calls/)).toBeVisible();
  });

  test('arXiv screening inspector exposes collaboration controls', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openFixture(page, 'arXiv screening report');
    const inspector = inspectorDialog(page);

    await expect(inspector.getByText('Thin coverage')).toBeVisible();
    await expect(inspector.getByRole('button', { name: 'Fill gaps' })).toBeVisible();
    await expect(inspector.getByRole('button', { name: 'Export BibTeX' })).toBeVisible();
    await expect(inspector.getByText('Working Set')).toBeVisible();
    await expect(inspector.getByText('SWEEP').first()).toBeVisible();
    await expect(inspector.getByText('REFINE')).toBeVisible();
  });

  test('arXiv incomplete screening is not reported as zero kept', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const incompleteSection = page.locator('section').filter({ hasText: 'arXiv incomplete screening' });
    const arxivGroup = incompleteSection.getByRole('button', { name: /Searched arXiv/ });
    await expect(arxivGroup).toContainText('screening incomplete');
    await expect(arxivGroup).not.toContainText('0 agent-kept');

    await openFixture(page, 'arXiv incomplete screening report');
    const inspector = inspectorDialog(page);
    await expect(inspector.getByText('40 candidates collected · 0 screened')).toBeVisible();
    await expect(inspector.getByText('No agent screening verdict was emitted')).toHaveCount(3);
  });

  test('arXiv grouped screening reports open inspectable report details', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const duplicateSection = page.locator('section').filter({ hasText: 'arXiv duplicate screening reports' });
    const arxivGroup = duplicateSection.getByRole('button', { name: /Searched arXiv/ });
    await arxivGroup.click();

    const screeningGroup = duplicateSection.getByRole('button', { name: /2 screening reports/ }).first();
    await expect(screeningGroup).toBeVisible();
    await expect(duplicateSection.getByText('Evidence unverified')).toBeVisible();

    await screeningGroup.click();
    const inspector = inspectorDialog(page);
    await expect(inspector).toBeVisible();
    await expect(inspector.getByText('arXiv research audit').first()).toBeVisible();
    await expect(inspector.getByText('Screening evidence is unverified: 0 of 3 papers have validated evidence spans.')).toBeVisible();
    await expect(inspector.getByText('Working Set')).toBeVisible();
    await expect(inspector.getByText('screened out')).toBeVisible();
    await expect(inspector.getByText('screening report 2: 0 of 3 papers have validated evidence spans.')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(inspector).not.toBeVisible();

    await duplicateSection.getByRole('button', { name: 'Toggle grouped arXiv rows' }).click();
    const firstReport = duplicateSection.getByRole('button', { name: /screening report 1/ });
    await expect(firstReport).toContainText('superseded');
    await expect(firstReport).toContainText('0 verified spans');

    await firstReport.click();
    const firstReportInspector = inspectorDialog(page);
    await expect(firstReportInspector.getByText('screening report 1: 0 of 3 papers have validated evidence spans.')).toBeVisible();
    await expect(firstReportInspector.getByText('Validation Warnings')).toBeVisible();
  });

  test('arXiv recommendations render as a structured artifact', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });
    await openFixture(page, 'arXiv recommendations');
    const inspector = inspectorDialog(page);

    await expect(inspector.getByText('arXiv recommendations').first()).toBeVisible();
    await expect(inspector.getByText('Recommendation Set')).toBeVisible();
    await expect(inspector.getByText('report call_arxiv_duplicate_report_2')).toBeVisible();
    await expect(inspector.getByText('Best direct fit for reviewing search-trace and screening workflow design.')).toBeVisible();
    await expect(inspector.getByText('Use as the starting point for agentic literature-search audit UI patterns.')).toBeVisible();
    await expect(inspector.getByText('evidence 1 · summary')).toBeVisible();
    await expect(inspector.getByText('Recommendation evidence references point to a report with zero validated evidence spans.')).toBeVisible();
    await expect(inspector.getByText('x2')).toBeVisible();
    await expect(inspector.getByText('2 papers')).toBeVisible();
  });

  test('arXiv chunked text reads collapse internal helper noise', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const chunkedSection = page.locator('section').filter({ hasText: 'arXiv chunked paper reading' });
    await chunkedSection.getByRole('button', { name: /Searched arXiv/ }).click();

    const sessionCard = chunkedSection.getByTestId('arxiv-read-session-card');
    await expect(sessionCard).toHaveCount(1);
    await expect(sessionCard).toContainText('Agentic Search for Scientific Discovery');
    await expect(sessionCard).toContainText('3 read windows');
    await expect(sessionCard).toContainText('60,000 chars');
    await expect(sessionCard).toContainText('through 100%');
    await expect(sessionCard).toContainText('pdf text');
    await expect(sessionCard).toContainText('verdict: kept');
    await expect(chunkedSection.getByTestId('arxiv-clean-read-card')).toHaveCount(0);
    await expect(chunkedSection).not.toContainText('search_artifact_content');
    await expect(chunkedSection).not.toContainText('get_json_artifact');
    await expect(chunkedSection).not.toContainText('arxiv_get_paper_text result');
  });

  test('arXiv completed paper read answers which paper, which part, and verification', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const workflowSection = page.locator('section').filter({ hasText: 'arXiv research workflow' });
    await workflowSection.getByRole('button', { name: /Searched arXiv/ }).click();

    // Title is the human anchor; the deterministic locator line sits under it.
    const readCard = workflowSection
      .getByRole('button', { name: /Open full audit — arxiv_read_paper/ })
      .filter({ hasText: 'Agentic Search for Scientific Discovery' });
    const readLifecycleCard = workflowSection
      .getByTestId('arxiv-clean-read-card')
      .filter({ hasText: 'Agentic Search for Scientific Discovery' });
    await expect(readLifecycleCard).toHaveAttribute('data-state', 'settled');
    await expect(readLifecycleCard).toHaveAttribute('data-beat', 'settled');
    await expect(readCard).toContainText('arXiv:2508.08077 · pp. 7–9 / 12');
    await expect(readCard).toContainText('READ');

    // Factual state pill (not a generic green Success) on every read row, and
    // deterministic coverage from paper_pages: ruler caption, extract
    // quality, source kind.
    await expect(workflowSection.getByText('Read', { exact: true })).toHaveCount(2);
    await expect(workflowSection.getByText('3 of 12 pages (25%)')).toBeVisible();
    await expect(workflowSection.getByText('pdf extract').first()).toBeVisible();
    await expect(workflowSection.getByText('more pages remain · next p. 10')).toBeVisible();
    await expect(workflowSection.getByText('untrusted paper text').first()).toBeVisible();

    // Joined screening trace at rest: provenance, agent verdict, one labeled
    // rationale sentence, span-lifecycle chip, and indexed Details counts —
    // the evidence excerpts themselves live one level down, not on the card.
    await expect(workflowSection.getByText('found by 2 queries').first()).toBeVisible();
    await expect(workflowSection.getByText('verdict: kept')).toBeVisible();
    await expect(workflowSection.getByText(/Verdict rationale \(agent\):/)).toBeVisible();
    await expect(workflowSection.getByText('2 spans verified')).toBeVisible();
    await expect(workflowSection.getByText('Details — 2 evidence spans · 1 source flag')).toBeVisible();
    await expect(workflowSection.getByText('every verdict must cite an exact evidence span from the paper text')).toHaveCount(0);
    await expect(workflowSection.getByTestId('tool-chart-artifact-card')).toHaveCount(0);
    await expect(workflowSection.getByTestId('sandboxed-html-artifact-card')).toHaveCount(0);

    // Click-through detail view: identity first, position strip, then the
    // evidence excerpts behind the indexed Details expander.
    await readCard.click();
    const inspector = inspectorDialog(page);
    await expect(inspector).toBeVisible();
    await expect(inspector.getByText('arXiv paper read')).toBeVisible();
    await expect(inspector.getByText('read complete')).toBeVisible();
    await expect(inspector.getByText('pp. 7–9 of 12 · 3 pages extracted · pdftotext')).toBeVisible();
    await expect(inspector.getByText('external source')).toBeVisible();
    await expect(inspector.getByText(/Verdict rationale \(agent\):/)).toBeVisible();

    await inspector.getByRole('button', { name: /Details/ }).click();
    await expect(inspector.getByText('every verdict must cite an exact evidence span from the paper text')).toBeVisible();
    await expect(inspector.getByText('span verified').first()).toBeVisible();
    await expect(inspector.getByText('verbatim · untrusted external text').first()).toBeVisible();
    await expect(inspector.getByText(/window stopped early · next p\. 10/)).toBeVisible();

    await inspector.getByRole('button', { name: /Paper text/ }).click();
    await expect(inspector.getByText('Page 7', { exact: true })).toBeVisible();
    await expect(inspector.getByRole('button', { name: 'raw', exact: true })).toBeVisible();
    await page.keyboard.press('Escape');
  });

  test('arXiv scan read speaks the scan register and never manufactures findings', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const workflowSection = page.locator('section').filter({ hasText: 'arXiv research workflow' });
    await workflowSection.getByRole('button', { name: /Searched arXiv/ }).click();

    // Identity first: title, SCAN tag, one-line gloss, deterministic locator.
    const scanCard = workflowSection
      .getByRole('button', { name: /Open full audit — arxiv_read_paper/ })
      .filter({ hasText: 'Workflow Traces for Human-AI Research Collaboration' });
    await expect(scanCard).toContainText('SCAN');
    await expect(scanCard).toContainText('Proposes a trace schema that makes human-AI research workflows replayable and auditable.');
    await expect(scanCard).toContainText('arXiv:2401.00320 · pp. 10–11 / 11');

    // One observation sentence in noticed/observed language, plus indexed
    // counts for the notes and chase candidates.
    await expect(workflowSection.getByText(/Noticed: Back matter only/)).toBeVisible();
    await expect(workflowSection.getByText('observations, not conclusions')).toBeVisible();
    await expect(workflowSection.getByText('Details — 3 notes · 2 chase candidates')).toBeVisible();
    await expect(workflowSection.getByText('2 of 11 pages (18%)')).toBeVisible();

    // The screening layer keeps its labeled verdict chip, but the scan must
    // not render the paper's screening evidence or rationale as findings.
    await expect(workflowSection.getByText('verdict: possible')).toBeVisible();
    await expect(workflowSection.getByText('capturing task intent, tool parameters, retrieved identifiers')).toHaveCount(0);
    await expect(workflowSection.getByText(/Relevant to trace capture/)).toHaveCount(0);
    await expect(workflowSection.getByText(/Looking for:/)).toHaveCount(0);

    // Detail view stays in the scan register: notes and chases, no evidence
    // excerpts, raw paper text one expander away.
    await scanCard.click();
    const inspector = inspectorDialog(page);
    await expect(inspector).toBeVisible();
    await expect(inspector.getByText(/Noticed: Back matter only/)).toBeVisible();
    await expect(inspector.getByText('observations, not conclusions')).toBeVisible();

    await inspector.getByRole('button', { name: /Details/ }).click();
    await expect(inspector.getByText('noticed · low-commitment')).toBeVisible();
    await expect(inspector.getByText('Appendix B is a field-by-field data dictionary for the trace schema.')).toBeVisible();
    await expect(inspector.getByText('chase candidates', { exact: true })).toBeVisible();
    await expect(inspector.getByRole('button', { name: 'Queue search' }).first()).toBeVisible();
    await expect(inspector.getByText('span verified')).toHaveCount(0);
    await expect(inspector.getByText('capturing task intent, tool parameters, retrieved identifiers')).toHaveCount(0);

    await inspector.getByRole('button', { name: /Paper text/ }).click();
    await expect(inspector.getByText(/APPENDIX B\. Trace field dictionary/)).toBeVisible();
    await expect(inspector.getByText('raw extract, unmodified — exactly what entered the model\'s context')).toBeVisible();
    await page.keyboard.press('Escape');
  });

  test('arXiv live paper reading shows page progress clearly', async ({ page }) => {
    await page.goto(ROUTE, { waitUntil: 'domcontentloaded' });

    const liveSection = page.locator('section').filter({ hasText: 'arXiv live paper reading' });
    const liveCard = liveSection.getByTestId('arxiv-clean-read-card');
    await expect(liveCard).toHaveAttribute('data-state', 'running');
    await expect(liveCard).toHaveAttribute('data-beat', 'read');
    await expect(liveSection).toContainText('Agentic Search for Scientific Discovery');
    await expect(liveSection).toContainText('Studies agentic literature search systems');
    await expect(liveSection).toContainText('Checking the pages most likely to justify the screening verdict.');
    await expect(liveSection).toContainText('arXiv:2508.08077');
    await expect(liveSection).toContainText('pp. 3–6 / 12');
    await expect(liveSection).toContainText('TARGETED');
    await expect(liveSection).toContainText('untrusted paper text');
    await expect(liveSection).toContainText('page 3 of 12');
    await expect(liveCard.getByRole('status')).toContainText('Reading page 3 of 12');
    await expect(liveCard.getByText(/^Details —/)).toHaveCount(0);
    await expect(liveCard.getByText(/Verdict rationale \(agent\):/)).toHaveCount(0);
    await expect(liveCard.getByText(/Found:/)).toHaveCount(0);

    const progress = liveSection.getByRole('progressbar', { name: 'Paper reading progress' });
    await expect(progress).toBeVisible();
    await expect(progress).toHaveAttribute('aria-valuenow', '34');
    await expect(progress).toHaveAttribute('aria-valuetext', /page 3 of 12/);
    await expect(liveSection.getByTestId('tool-chart-artifact-card')).toHaveCount(0);
    await expect(liveSection.getByTestId('sandboxed-html-artifact-card')).toHaveCount(0);
  });
});
