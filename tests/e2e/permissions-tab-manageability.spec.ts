import { expect, test, type Page } from '@playwright/test';

type FixturePermissionRequest = {
  method: string;
  path: string;
  body?: {
    category?: string;
    effect?: string;
    subject?: Record<string, unknown>;
  };
};

type FixtureApprovalDecision = {
  id: string;
  decision?: {
    action?: string;
    scope?: string;
    subject?: Record<string, unknown>;
  };
};

async function waitForPermissionRequest(
  page: Page,
  predicate: (request: FixturePermissionRequest) => boolean,
): Promise<FixturePermissionRequest> {
  let match: FixturePermissionRequest | undefined;
  await expect
    .poll(async () => {
      const requests = await page.evaluate(
        () => ((window as any).__approvalsFixturePermissionRequests || []) as FixturePermissionRequest[],
      );
      match = requests.find(predicate);
      return Boolean(match);
    })
    .toBe(true);
  return match as FixturePermissionRequest;
}

async function waitForToolTestRequest(
  page: Page,
  predicate: (request: any) => boolean,
): Promise<any> {
  let match: any;
  await expect
    .poll(async () => {
      const requests = await page.evaluate(
        () => ((window as any).__approvalsFixtureToolTestRequests || []) as any[],
      );
      match = requests.find(predicate);
      return Boolean(match);
    })
    .toBe(true);
  return match;
}

async function waitForApprovalDecision(
  page: Page,
  predicate: (decision: FixtureApprovalDecision) => boolean,
): Promise<FixtureApprovalDecision> {
  let match: FixtureApprovalDecision | undefined;
  await expect
    .poll(async () => {
      const decisions = await page.evaluate(
        () => ((window as any).__approvalsFixtureDecisions || []) as FixtureApprovalDecision[],
      );
      match = decisions.find(predicate);
      return Boolean(match);
    })
    .toBe(true);
  return match as FixtureApprovalDecision;
}

function permissionTestIdPart(value: string): string {
  return String(value || 'item')
    .replace(/[^a-zA-Z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '') || 'item';
}

test.beforeEach(async ({ page }) => {
  await page.goto('/dev/approvals', { waitUntil: 'domcontentloaded' });
  await expect(page.getByTestId('permissions-global-scope')).toBeVisible();
});

test('local approval card is picker-backed before approving a concrete folder grant', async ({ page }) => {
  const localApproval = page
    .locator('section')
    .filter({ hasText: 'Pending read — local folder access' })
    .first();

  await expect(localApproval.getByText('Choose where to search on this computer')).toBeVisible();
  await expect(localApproval.getByText('The assistant can request access, but you choose the folder or this whole computer.')).toBeVisible();
  await expect(localApproval.getByRole('button', { name: 'Search this computer' })).toBeVisible();
  await expect(localApproval.getByText('“Q3 forecast”', { exact: true })).toBeVisible();
  await expect(localApproval.getByText('Documents')).toBeVisible();
  await expect(localApproval.getByText('presentation, spreadsheet')).toBeVisible();
  await expect(localApproval.getByText('Read-only search handled by the local file search agent.')).toBeVisible();

  await localApproval.getByRole('button', { name: 'Choose folder' }).click();
  await expect(localApproval.getByText('~/Documents')).toBeVisible();
  await expect(localApproval.getByText('The assistant can request access, but you choose the folder or this whole computer.')).toHaveCount(0);
  await localApproval.getByRole('radio', { name: 'Always allow this folder' }).click();
  await localApproval.getByRole('button', { name: 'Allow folder search' }).click();

  const decision = await waitForApprovalDecision(page, (item) =>
    item.id === 'apr-local-docs' &&
    item.decision?.action === 'approve' &&
    item.decision?.scope === 'always' &&
    item.decision?.subject?.kind === 'local_location' &&
    item.decision?.subject?.local_root === '/Users/alex/Documents',
  );
  expect(decision.decision?.subject).toMatchObject({
    kind: 'local_location',
    local_root: '/Users/alex/Documents',
    local_kind: 'folder',
  });
});

test('local approval card can approve search across this computer without typed paths', async ({ page }) => {
  const localApproval = page
    .locator('section')
    .filter({ hasText: 'Pending read — local folder access' })
    .first();

  await localApproval.getByRole('button', { name: 'Search this computer' }).click();
  await expect(localApproval.getByText('This computer', { exact: true })).toBeVisible();
  await expect(localApproval.getByText('Can search all readable folders on this computer, except protected paths listed below.')).toBeVisible();
  await localApproval.getByRole('button', { name: 'Allow computer search' }).click();

  const decision = await waitForApprovalDecision(page, (item) =>
    item.id === 'apr-local-docs' &&
    item.decision?.action === 'approve' &&
    item.decision?.scope === 'once' &&
    item.decision?.subject?.kind === 'local_location' &&
    item.decision?.subject?.local_root === '/',
  );
  expect(decision.decision?.subject).toMatchObject({
    kind: 'local_location',
    local_root: '/',
    local_kind: 'folder',
  });
});

test('virtual provider tool connections save exact hosted-tool permission subjects', async ({ page }) => {
  await expect(page.getByTestId('permissions-tool-group-conn-web-search')).toBeVisible();
  await expect(page.getByTestId('permissions-tool-group-conn-python-sandbox')).toBeVisible();

  await page.getByTestId('permissions-tool-group-effect-conn-web-search-deny').click();
  for (const [tool, connector] of [
    ['anthropic-messages/web_search', 'anthropic-messages'],
    ['openai-responses/web_search', 'openai-responses'],
  ] as const) {
    const hostedToolRequest = await waitForPermissionRequest(page, (request) => {
      const subject = request.body?.subject || {};
      return request.method === 'POST' &&
        request.body?.effect === 'deny' &&
        request.body?.category === 'read' &&
        subject.kind === 'provider_hosted_tool' &&
        subject.tool === tool &&
        subject.tool_set_id === 'provider-hosted' &&
        subject.connector === connector &&
        subject.operation === 'webSearch';
    });

    expect(hostedToolRequest.body?.subject).toMatchObject({
      kind: 'provider_hosted_tool',
      tool,
      tool_set_id: 'provider-hosted',
      connector,
      operation: 'webSearch',
    });
  }
  const permissionRequests = await page.evaluate(
    () => ((window as any).__approvalsFixturePermissionRequests || []) as FixturePermissionRequest[],
  );
  expect(permissionRequests).not.toContainEqual(expect.objectContaining({
    method: 'POST',
    body: expect.objectContaining({
      subject: { kind: 'connector', connector: 'web-search' },
    }),
  }));

  await page.getByTestId('permissions-tool-group-toggle-conn-python-sandbox').click();
  await expect(page.getByTestId('permissions-tool-row-anthropic-messages-code_execution')).toBeVisible();

  await page.getByTestId('permissions-tool-effect-anthropic-messages-code_execution-allow').click();
  const atomicRequest = await waitForPermissionRequest(page, (request) => {
    const subject = request.body?.subject || {};
    return request.method === 'POST' &&
      request.body?.effect === 'allow' &&
      request.body?.category === 'execute' &&
      subject.kind === 'provider_hosted_tool' &&
      subject.tool === 'anthropic-messages/code_execution';
  });

  expect(atomicRequest.body?.subject).toMatchObject({
    kind: 'provider_hosted_tool',
    tool: 'anthropic-messages/code_execution',
    connector: 'anthropic-messages',
    tool_set_id: 'provider-hosted',
    operation: 'codeExecution',
  });
});

test('provider-hosted tools run a non-executing settings check through the body endpoint', async ({ page }) => {
  await page.getByTestId('permissions-tool-group-toggle-conn-web-search').click();
  await page.getByTestId('permissions-tool-toggle-openai-responses-web_search').click();

  const testButton = page.getByTestId('permissions-tool-test-openai-responses-web_search');
  await expect(testButton).toBeEnabled();
  await testButton.click();
  await expect(page.getByTestId('permissions-tool-test-message-openai-responses-web_search')).toContainText('Test passed.');

  const request = await waitForToolTestRequest(page, (request) =>
    request.path === '/api/tools/test' &&
    request.toolName === 'openai-responses/web_search' &&
    request.body?.tool_name === 'openai-responses/web_search',
  );
  expect(request.body).toMatchObject({ tool_name: 'openai-responses/web_search' });
});

test('tool tests respect permissions and run after Always allow', async ({ page }) => {
  await page.getByTestId('permissions-tool-group-toggle-conn-github').click();
  await page.getByTestId('permissions-tool-toggle-github_clone_repository').click();

  await page
    .getByTestId('permissions-tool-test-input-github_clone_repository-repository')
    .fill('openai/codex');
  await page.getByTestId('permissions-tool-test-github_clone_repository').click();
  await expect(page.getByTestId('permissions-tool-test-message-github_clone_repository')).toContainText(
    'set to Ask',
  );

  await page.getByTestId('permissions-tool-effect-github_clone_repository-allow').click();
  await waitForPermissionRequest(page, (request) => {
    const subject = request.body?.subject || {};
    return request.method === 'POST' &&
      request.body?.effect === 'allow' &&
      subject.kind === 'tool' &&
      subject.tool === 'github_clone_repository' &&
      subject.connector === 'github';
  });

  await page.getByTestId('permissions-tool-test-github_clone_repository').click();
  await expect(page.getByTestId('permissions-tool-test-message-github_clone_repository')).toContainText(
    'Test passed.',
  );
  const toolTestRequest = await waitForToolTestRequest(page, (request) =>
    request.toolName === 'github_clone_repository' &&
    request.body?.args?.repository === 'openai/codex',
  );
  expect(toolTestRequest.body.args).toEqual({ repository: 'openai/codex' });
});

test('local file search can be tested and locked connection tools stay disabled', async ({ page }) => {
  await page.getByTestId('permissions-tool-group-toggle-conn-local-computer').click();
  await page.getByTestId('permissions-tool-toggle-file_search').click();
  await expect(page.getByText('arg:local_root')).toHaveCount(0);
  await page.getByTestId('permissions-tool-test-choose-folder-file_search').click();
  await expect(page.getByTestId('permissions-tool-row-file_search').getByText('~/Documents')).toBeVisible();
  await page.getByTestId('permissions-tool-test-input-file_search-query').fill('Q3 forecast');
  await page.getByTestId('permissions-tool-test-file_search').click();
  await expect(page.getByTestId('permissions-tool-test-message-file_search')).toContainText('Test passed.');

  const localSearchRequest = await waitForToolTestRequest(page, (request) =>
    request.toolName === 'file_search' &&
    request.body?.args?.local_root === '/Users/alex/Documents' &&
    request.body?.args?.local_kind === 'folder' &&
    request.body?.args?.query === 'Q3 forecast',
  );
  expect(localSearchRequest.body.args).toMatchObject({
    local_root: '/Users/alex/Documents',
    local_kind: 'folder',
    query: 'Q3 forecast',
  });

  await page.getByTestId('permissions-tool-group-toggle-conn-slack').click();
  await page.getByTestId('permissions-tool-toggle-slack_post_message').click();
  await expect(page.getByTestId('permissions-tool-test-slack_post_message')).toBeDisabled();
});

test('local file grants are picker-backed and exact-file scoped', async ({ page }) => {
  const localSection = page
    .locator('section')
    .filter({ hasText: 'Read > Local files' })
    .first();

  await localSection.getByRole('button', { name: 'Choose file' }).click();
  await expect(localSection.getByText('~/Documents/Q3 forecast.xlsx', { exact: true })).toBeVisible();
  await expect(localSection.getByText('Selected file only')).toBeVisible();

  const createRequest = await waitForPermissionRequest(page, (request) => {
    const subject = request.body?.subject || {};
    return request.method === 'POST' &&
      request.path === '/api/permissions/rules' &&
      subject.kind === 'local_location' &&
      subject.local_root === '/Users/alex/Documents/Q3 forecast.xlsx' &&
      subject.local_kind === 'file';
  });
  expect(createRequest.body?.subject).toMatchObject({
    kind: 'local_location',
    local_root: '/Users/alex/Documents/Q3 forecast.xlsx',
    local_kind: 'file',
  });

  const fileRow = localSection
    .locator('li')
    .filter({ hasText: '~/Documents/Q3 forecast.xlsx' })
    .last();
  await fileRow.getByRole('button', { name: 'Test' }).click();
  const testRequest = await waitForPermissionRequest(page, (request) => {
    const subject = request.body?.subject || {};
    return request.method === 'POST' &&
      request.path === '/api/permissions/local-locations/test' &&
      subject.local_root === '/Users/alex/Documents/Q3 forecast.xlsx' &&
      subject.local_kind === 'file';
  });
  expect(testRequest.body?.subject).toMatchObject({
    kind: 'local_location',
    local_root: '/Users/alex/Documents/Q3 forecast.xlsx',
    local_kind: 'file',
  });
});

test('local read file tool test uses a file picker and hides local_root input', async ({ page }) => {
  await page.getByTestId('permissions-tool-group-toggle-conn-local-computer').click();
  await page.getByTestId('permissions-tool-toggle-local_read_file').click();
  await expect(page.getByText('arg:local_root')).toHaveCount(0);

  await page.getByTestId('permissions-tool-test-choose-file-local_read_file').click();
  await expect(page.getByTestId('permissions-tool-row-local_read_file').getByText('~/Documents/Q3 forecast.xlsx')).toBeVisible();
  await expect(page.getByTestId('permissions-tool-test-input-local_read_file-path')).toHaveValue('Q3 forecast.xlsx');

  await page.getByTestId('permissions-tool-test-local_read_file').click();
  await expect(page.getByTestId('permissions-tool-test-message-local_read_file')).toContainText('Test passed.');

  const request = await waitForToolTestRequest(page, (item) =>
    item.toolName === 'local_read_file' &&
    item.body?.args?.local_root === '/Users/alex/Documents/Q3 forecast.xlsx' &&
    item.body?.args?.local_kind === 'file' &&
    item.body?.args?.path === 'Q3 forecast.xlsx',
  );
  expect(request.body.args).toMatchObject({
    local_root: '/Users/alex/Documents/Q3 forecast.xlsx',
    local_kind: 'file',
    path: 'Q3 forecast.xlsx',
  });
});

test('every generated tool row is addressable from Settings permissions', async ({ page }) => {
  test.setTimeout(120000);
  const toolIds = await page.evaluate(async () => {
    const generated = await import('/lib/generatedToolMetadata.ts' as string);
    return [
      ...Array.from(generated.GENERATED_STATIC_TOOL_METADATA as any[]).map((tool) => tool.name),
      ...Array.from(generated.GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES as any[]).map(
        (capability) => capability.id,
      ),
    ].sort();
  });
  const search = page.getByLabel('Search tools by name or tool set');

  for (const toolId of toolIds) {
    const safeId = permissionTestIdPart(toolId);
    await search.fill(toolId);
    await expect(page.getByTestId(`permissions-tool-row-${safeId}`), toolId).toBeVisible();
    await expect(page.getByTestId(`permissions-tool-effect-${safeId}-deny`), `${toolId} Off control`).toBeEnabled();
    await expect(page.getByTestId(`permissions-tool-effect-${safeId}-ask`), `${toolId} Ask control`).toBeEnabled();
    await expect(page.getByTestId(`permissions-tool-effect-${safeId}-allow`), `${toolId} Always control`).toBeEnabled();
    await page.getByTestId(`permissions-tool-toggle-${safeId}`).click();
    await expect(page.getByTestId(`permissions-tool-test-${safeId}`), toolId).toHaveCount(1);
  }
});
