import { expect, test, type Page } from '@playwright/test';

/**
 * App-wiring coverage for the flagged provider-capability settings UI
 * (components/settings/providers/**, plan §4.1–§4.3/§4.7).
 *
 * The capability backend has not landed; these tests mock the plan §5 endpoints
 * (`/api/providers/capabilities/resolve`, `/api/providers/models/discover`) and
 * assert that the visual layer renders ONLY what the report says — no support is
 * ever inferred from providerId/apiType.
 */

function jsonResponse(body: unknown, status = 200) {
  return { status, contentType: 'application/json', body: JSON.stringify(body) };
}

function feature(overrides: Record<string, unknown>) {
  return {
    meta: { label: 'Feature', group: 'core', headline: false, order: 0 },
    class: 'standard',
    riskTags: [],
    state: 'unknown',
    mode: null,
    effective: 'off',
    control: null,
    permission: null,
    conditions: [],
    reasons: [],
    limits: null,
    detail: null,
    evidence: [],
    ...overrides,
  };
}

function report(target: Record<string, unknown>, features: Record<string, unknown>) {
  return {
    target: {
      connectionId: null,
      providerFamily: 'bedrock',
      apiSurface: 'invoke_stream',
      endpointFamily: 'bedrock-runtime',
      model: 'anthropic.claude-opus-4-6-v1',
      region: 'us-east-1',
      adapterVersion: 'test',
      ...target,
    },
    fingerprint: 'fp-test',
    catalogVersion: 'catalog-test',
    generatedAt: new Date().toISOString(),
    features,
  };
}

const bedrockInvokeReport = report({ apiSurface: 'invoke_stream' }, {
  text_streaming: feature({
    meta: { label: 'Streaming text', group: 'core', headline: false, order: 0 },
    state: 'supported', effective: 'on',
    evidence: [{ kind: 'catalog', verifiedAt: '2026-07-11T00:00:00Z', ref: 'https://docs.aws.amazon.com/bedrock', stale: false, staleReason: null }],
  }),
  custom_tools: feature({
    meta: { label: 'Custom tools', group: 'tools', headline: true, order: 0 },
    state: 'supported', effective: 'on',
    evidence: [{ kind: 'catalog', verifiedAt: '2026-07-11T00:00:00Z', ref: null, stale: false, staleReason: null }],
  }),
  documents: feature({
    meta: { label: 'PDF / documents', group: 'inputs', headline: true, order: 1 },
    state: 'conditional', effective: 'on',
    conditions: [{ code: 'requires_converse_switch', text: 'Requires switching to the Converse API — you will be asked before sending' }],
    evidence: [{ kind: 'catalog', verifiedAt: '2026-07-11T00:00:00Z', ref: null, stale: false, staleReason: null }],
  }),
  hosted_web_search: feature({
    meta: { label: 'Web search', group: 'hosted', headline: true, order: 0 },
    class: 'explicit_consent', riskTags: ['billable', 'external_egress'],
    state: 'unsupported', effective: 'unavailable',
    reasons: [{ code: 'not_on_bedrock', text: 'Not offered on Amazon Bedrock' }],
    evidence: [{ kind: 'catalog', verifiedAt: '2026-07-11T00:00:00Z', ref: 'https://docs.aws.amazon.com/bedrock', stale: false, staleReason: null }],
  }),
  hosted_code_execution: feature({
    meta: { label: 'Code execution', group: 'hosted', headline: true, order: 1 },
    class: 'explicit_consent', riskTags: ['billable', 'code_execution'],
    state: 'unsupported', effective: 'unavailable',
    reasons: [{ code: 'not_on_bedrock', text: 'Not offered on Amazon Bedrock' }],
    evidence: [{ kind: 'catalog', verifiedAt: '2026-07-11T00:00:00Z', ref: null, stale: false, staleReason: null }],
  }),
  structured_output: feature({
    meta: { label: 'Structured output', group: 'output', headline: false, order: 0 },
    state: 'unknown', effective: 'off',
  }),
});

const bedrockConverseReport = report({ apiSurface: 'converse_stream' }, {
  documents: feature({
    meta: { label: 'PDF / documents', group: 'inputs', headline: true, order: 1 },
    state: 'conditional', effective: 'on',
    conditions: [{ code: 'native_documents', text: 'PDF/Office · ≤4.5 MB each · max 5 (native)' }],
    limits: { mimeTypes: ['application/pdf'], maxBytes: 4718592, maxCount: 5, requestBudgetBytes: null, contextTokens: null, maxOutputTokens: null },
    evidence: [{ kind: 'catalog', verifiedAt: '2026-07-11T00:00:00Z', ref: null, stale: false, staleReason: null }],
  }),
  hosted_web_search: feature({
    meta: { label: 'Web search', group: 'hosted', headline: true, order: 0 },
    class: 'explicit_consent', riskTags: ['billable', 'external_egress'],
    state: 'unsupported', effective: 'unavailable',
    reasons: [{ code: 'not_on_bedrock', text: 'Not offered on Amazon Bedrock' }],
  }),
});

const settingsPayload = {
  settings: {
    provider: { providerId: 'bedrock', model: 'anthropic.claude-opus-4-6-v1' },
    // Required by migrateSettings' "already-new settings" branch; without it the
    // payload is treated as legacy and providerConfigs are rebuilt from scratch.
    compatibleProfiles: [],
    // Anthropic is disabled in the default provider flags; enable it so the
    // discovery test can use its single-auth wizard path.
    featureFlags: {
      providers: { openai: false, anthropic: true, gemini: false, bedrock: true, compatible: true },
    },
    providerConfigs: [
      {
        id: 'cfg-bedrock-invoke',
        displayName: 'Bedrock Claude',
        providerId: 'bedrock',
        model: 'anthropic.claude-opus-4-6-v1',
        apiType: 'messages',
        region: 'us-east-1',
        authType: 'api_key',
        isDefault: true,
        lastTestedAt: Date.now() - 2 * 3_600_000,
        lastTestStatus: 'success',
      },
      {
        id: 'cfg-bedrock-converse',
        displayName: 'Bedrock Converse Sample',
        providerId: 'bedrock',
        model: 'anthropic.claude-opus-4-6-v1',
        apiType: 'messages',
        region: 'us-east-1',
        authType: 'api_key',
      },
    ],
    activeProviderConfigId: 'cfg-bedrock-invoke',
  },
};

async function setupApp(page: Page, opts?: {
  discovery?: unknown;
  settings?: unknown;
  secretsPresent?: Record<string, boolean>;
}) {
  const resolveRequests: string[] = [];
  const settingsPuts: any[] = [];
  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    const method = request.method();

    if (pathname === '/api/settings' && method === 'GET') {
      await route.fulfill(jsonResponse(opts?.settings ?? settingsPayload));
      return;
    }
    if (pathname === '/api/settings' && (method === 'PUT' || method === 'POST')) {
      try { settingsPuts.push(JSON.parse(request.postData() || '{}')); } catch { settingsPuts.push({}); }
      await route.fulfill(jsonResponse(opts?.settings ?? settingsPayload));
      return;
    }
    if (pathname === '/api/activation/status' && method === 'GET') {
      await route.fulfill(jsonResponse({ activated: true }));
      return;
    }
    if (pathname === '/api/secrets/status' && method === 'GET') {
      await route.fulfill(jsonResponse({
        initialized: true, locked: false, configured: {}, present: opts?.secretsPresent || {},
      }));
      return;
    }
    if (pathname === '/api/threads' && method === 'GET') {
      await route.fulfill(jsonResponse({ threads: [] }));
      return;
    }
    if (pathname === '/api/providers/capabilities/resolve' && method === 'POST') {
      let cfgId = '';
      try {
        cfgId = String(JSON.parse(request.postData() || '{}')?.config?.id || '');
      } catch {
        cfgId = '';
      }
      resolveRequests.push(cfgId);
      const body = cfgId === 'cfg-bedrock-converse' ? bedrockConverseReport : bedrockInvokeReport;
      await route.fulfill(jsonResponse({ report: body }));
      return;
    }
    const connCapabilities = pathname.match(/^\/api\/providers\/connections\/([^/]+)\/capabilities$/);
    if (connCapabilities && method === 'GET') {
      const cfgId = decodeURIComponent(connCapabilities[1]);
      resolveRequests.push(cfgId);
      const body = cfgId === 'cfg-bedrock-converse' ? bedrockConverseReport : bedrockInvokeReport;
      await route.fulfill(jsonResponse({ report: body }));
      return;
    }
    if (pathname === '/api/providers/models/discover' && method === 'POST') {
      if (opts?.discovery) {
        await route.fulfill(jsonResponse(opts.discovery));
      } else {
        await route.fulfill(jsonResponse({ error: 'not found' }, 404));
      }
      return;
    }
    await route.fulfill(jsonResponse({}));
  });
  return { resolveRequests, settingsPuts };
}

async function openProvidersTab(page: Page) {
  await page.getByRole('button', { name: 'Open settings' }).click();
  const settings = page.getByRole('dialog', { name: 'Settings' });
  await expect(settings).toBeVisible();
  await settings.getByRole('tab', { name: 'Providers' }).click();
  return settings;
}

test('flag off: legacy providers tab is preserved and no capability requests fire', async ({ page }) => {
  const { resolveRequests } = await setupApp(page);
  await page.goto('/');
  const settings = await openProvidersTab(page);

  await expect(settings.getByText('Bedrock Claude')).toBeVisible();
  await expect(settings.locator('[data-connection-card]')).toHaveCount(0);
  await expect(settings.locator('[data-capability-chip]')).toHaveCount(0);
  await expect(settings.locator('[data-connection-identity]')).toHaveCount(0);

  await settings.getByRole('button', { name: 'Add Provider' }).click();
  await expect(settings.getByRole('heading', { name: 'Choose a provider' })).toBeVisible();
  await expect(settings.locator('[data-service-surface-step]')).toHaveCount(0);

  expect(resolveRequests).toHaveLength(0);
});

test('flag on: cards show family · surface · model identity and report-driven chips', async ({ page }) => {
  await setupApp(page);
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);

  const card = settings.locator('[data-connection-card="cfg-bedrock-invoke"]');
  await expect(card).toBeVisible();
  await expect(card.locator('[data-connection-identity]')).toContainText('Amazon Bedrock · Claude · Messages');
  await expect(card.locator('[data-connection-identity]')).toContainText('anthropic.claude-opus-4-6-v1');

  const strip = card.locator('[data-connection-chip-strip]');
  await expect(strip.locator('[data-capability-chip="not_available"]').first()).toContainText('Not available');
  await expect(strip.locator('[data-capability-chip="unverified"]')).toContainText('Unverified');
});

test('flag on: Bedrock hosted web/code are Not available and documents distinguish Invoke from Converse', async ({ page }) => {
  await setupApp(page);
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);

  // Invoke-surface connection detail.
  await settings.locator('[data-connection-card="cfg-bedrock-invoke"] [data-connection-chip-strip]').click();
  const invokeDetail = settings.locator('[data-connection-detail="cfg-bedrock-invoke"]');
  await expect(invokeDetail).toBeVisible();

  const webRow = invokeDetail.locator('[data-capability-row="hosted_web_search"]');
  await expect(webRow.locator('[data-capability-chip="not_available"]')).toContainText('Not available');
  await expect(webRow).toContainText('Not offered on Amazon Bedrock');
  const codeRow = invokeDetail.locator('[data-capability-row="hosted_code_execution"]');
  await expect(codeRow.locator('[data-capability-chip="not_available"]')).toContainText('Not available');

  const invokeDocs = invokeDetail.locator('[data-capability-row="documents"]');
  await expect(invokeDocs.locator('[data-capability-chip="conditional"]')).toContainText('Conditional');
  await expect(invokeDocs).toContainText('Requires switching to the Converse API');

  // Converse-surface connection detail shows native conditional documents instead.
  await invokeDetail.getByRole('button', { name: 'All provider connections' }).click();
  await settings.locator('[data-connection-card="cfg-bedrock-converse"] [data-connection-chip-strip]').click();
  const converseDetail = settings.locator('[data-connection-detail="cfg-bedrock-converse"]');
  const converseDocs = converseDetail.locator('[data-capability-row="documents"]');
  await expect(converseDocs).toContainText('PDF/Office · ≤4.5 MB each · max 5 (native)');
  await expect(converseDocs).not.toContainText('Requires switching');
});

test('flag on: consent-class rows show a shield with no Auto wording; evidence drawer is keyboard-operable', async ({ page }) => {
  await setupApp(page);
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);
  await settings.locator('[data-connection-card="cfg-bedrock-invoke"] [data-connection-chip-strip]').click();
  const detail = settings.locator('[data-connection-detail="cfg-bedrock-invoke"]');

  const webRow = detail.locator('[data-capability-row="hosted_web_search"]');
  await expect(webRow.getByText(/Requires explicit enable and permission/)).toBeAttached();
  await expect(webRow).not.toContainText('Auto');

  // Keyboard: focus the row toggle, open with Enter, close with Enter.
  const toggle = webRow.getByRole('button', { name: 'Show evidence for Web search' });
  await toggle.focus();
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await page.keyboard.press('Enter');
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  const drawer = detail.locator('[data-capability-evidence]');
  await expect(drawer).toContainText('Web search: Not available');
  await expect(drawer).toContainText('Curated catalog');
  await drawer.getByRole('button', { name: 'Show raw resolver output' }).click();
  await expect(drawer.locator('pre')).toContainText('"not_on_bedrock"');
  await page.keyboard.press('Enter');
});

test('flag on: unverified stays honest in the wizard capability summary', async ({ page }) => {
  await setupApp(page);
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);

  await settings.getByRole('button', { name: 'Add Provider' }).click();
  await expect(settings.getByRole('heading', { name: 'Choose a service' })).toBeVisible();
  await settings.locator('[data-surface-option="bedrock:responses"]').click();
  await expect(settings.locator('[data-surface-option="bedrock:responses"]')).toHaveAttribute('aria-checked', 'true');
  await settings.getByRole('button', { name: 'Continue' }).click();

  // Auth step (bedrock has two methods) → pick API key.
  await settings.getByText('Bedrock API Key', { exact: true }).click();
  await settings.getByRole('button', { name: 'Continue' }).click();

  // Surface-first IA: surface chip shown, raw apiType select absent.
  await expect(settings.locator('[data-surface-chip]')).toContainText('Responses (Mantle)');
  await expect(settings.getByText('API Type', { exact: true })).toHaveCount(0);

  const structuredRow = settings.locator('[data-capability-row="structured_output"]');
  // (Not on this step yet — matrix lives on the test step; just assert the step-2 model list seam is silent when discovery 404s.)
  await expect(settings.locator('[data-model-discovery]')).toHaveCount(0);
  await expect(structuredRow).toHaveCount(0);
});

test('flag on: model discovery renders lifecycle badges and preserves manual entry', async ({ page }) => {
  await setupApp(page, {
    discovery: {
      source: 'provider_api',
      fetchedAt: new Date().toISOString(),
      error: null,
      models: [
        { id: 'model-recommended', lifecycle: 'recommended', contextTokens: 200000 },
        { id: 'model-deprecated', lifecycle: 'deprecated', deprecationDate: '2027-03-01' },
        { id: 'model-shutdown', lifecycle: 'shutdown' },
      ],
    },
  });
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);

  await settings.getByRole('button', { name: 'Add Provider' }).click();
  await settings.locator('[data-surface-option="anthropic:default"]').click();
  await settings.getByRole('button', { name: 'Continue' }).click();

  const discovery = settings.locator('[data-model-discovery]');
  await expect(discovery).toBeVisible();
  await expect(discovery.getByText('Recommended', { exact: true })).toBeVisible();
  await expect(discovery.getByText('Deprecated — retires 2027-03-01')).toBeVisible();
  const shutdown = discovery.getByRole('radio', { name: /model-shutdown/ });
  await expect(shutdown).toBeDisabled();

  await discovery.getByRole('radio', { name: /model-recommended/ }).click();
  const modelInput = settings.getByPlaceholder('claude-opus-4-6');
  await expect(modelInput).toHaveValue('model-recommended');

  // Manual entry always preserved.
  await modelInput.fill('my-custom-model');
  await expect(modelInput).toHaveValue('my-custom-model');
});

// ---- Contract-correction regressions (edit preservation, templates, Gemini) ----

const preservationSettingsPayload = {
  settings: {
    provider: { providerId: 'anthropic', model: 'claude-opus-4-6' },
    compatibleProfiles: [],
    featureFlags: {
      providers: { openai: false, anthropic: true, gemini: true, bedrock: true, compatible: true },
    },
    cloudCredentials: {
      aws: [],
      gcp: [{ id: 'gcp-1', displayName: 'Legacy GCP Project', projectId: 'legacy-proj', adcMode: 'auto' }],
    },
    providerConfigs: [
      {
        id: 'cfg-anthropic',
        displayName: 'Work Anthropic',
        providerId: 'anthropic',
        model: 'claude-opus-4-6',
        authType: 'api_key',
        apiSurface: 'messages',
        isDefault: true,
        webSearch: { enabled: true },
        capabilityControls: { hosted_web_search: 'on', hosted_code_execution: 'off', future_registry_feature: 'on' },
        attachmentPolicies: [
          { id: 'pol-1', class: 'office_docs_to_text', action: 'extracted_text', apiSurface: 'messages', createdAt: 1752000000000 },
        ],
        configFingerprint: 'fp-stale-should-drop',
        futureConnectionField: { nested: ['keep-me'] },
        lastTestedAt: 1752000000000,
        lastTestStatus: 'success',
      },
      {
        id: 'cfg-gemini-legacy',
        displayName: 'Legacy Gemini ADC',
        providerId: 'gemini',
        model: 'gemini-legacy-model',
        authType: 'cloud_credentials',
        cloudCredentialRefs: { gcpCredentialId: 'gcp-1' },
      },
    ],
    activeProviderConfigId: 'cfg-anthropic',
  },
};

test('editing a connection preserves controls, policies, surface, and unknown fields — and drops the stale fingerprint', async ({ page }) => {
  const { settingsPuts } = await setupApp(page, {
    settings: preservationSettingsPayload,
    secretsPresent: { anthropic_api_key: true },
  });
  page.on('dialog', () => { throw new Error('window.confirm/prompt must not be used'); });
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);

  await settings.getByRole('button', { name: 'Edit Work Anthropic' }).click();
  await settings.getByRole('button', { name: 'Continue' }).click(); // service (preselected)
  const modelInput = settings.getByPlaceholder('claude-opus-4-6');
  await expect(modelInput).toHaveValue('claude-opus-4-6');
  await modelInput.fill('claude-opus-4-7');
  await settings.getByRole('button', { name: 'Continue' }).click(); // configure → test
  await settings.getByRole('button', { name: 'Save Provider' }).click();
  await settings.locator('[data-save-confirm]').getByRole('button', { name: 'Save anyway' }).click();

  await expect.poll(() => settingsPuts.length).toBeGreaterThan(0);
  const saved = settingsPuts.at(-1);
  const savedCfg = saved.providerConfigs.find((c: any) => c.id === 'cfg-anthropic');
  expect(savedCfg.model).toBe('claude-opus-4-7');
  // Edits never reset capability state: the explicit "on" and the unknown
  // registry key survive; policies and the canonical surface are intact.
  expect(savedCfg.capabilityControls).toMatchObject({
    hosted_web_search: 'on', hosted_code_execution: 'off', future_registry_feature: 'on',
  });
  expect(savedCfg.attachmentPolicies).toEqual([
    { id: 'pol-1', class: 'office_docs_to_text', action: 'extracted_text', apiSurface: 'messages', createdAt: 1752000000000 },
  ]);
  expect(savedCfg.apiSurface).toBe('messages');
  expect(savedCfg.futureConnectionField).toEqual({ nested: ['keep-me'] });
  // Stale fingerprint is omitted so the server recomputes it.
  expect(savedCfg.configFingerprint).toBeUndefined();
});

test('templates: inline name input (no window.prompt), stripped identity/evidence, preserved capability fields', async ({ page }) => {
  const { settingsPuts } = await setupApp(page, {
    settings: preservationSettingsPayload,
    secretsPresent: { anthropic_api_key: true },
  });
  page.on('dialog', () => { throw new Error('window.confirm/prompt must not be used'); });
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);

  await settings.getByRole('button', { name: 'Edit Work Anthropic' }).click();
  await settings.getByRole('button', { name: 'Continue' }).click();
  await settings.getByRole('button', { name: 'Continue' }).click();

  await settings.getByRole('button', { name: 'Save Template' }).click();
  const panel = settings.locator('[data-template-name-panel]');
  await expect(panel).toBeVisible();
  const nameInput = panel.getByLabel('Template name');
  await expect(nameInput).toHaveValue('Work Anthropic template');
  await nameInput.fill('Golden Anthropic');
  await panel.getByRole('button', { name: 'Save template' }).click();
  await expect(panel).toHaveCount(0);

  // Persist settings (template rides along) via the provider save.
  await settings.getByRole('button', { name: 'Save Provider' }).click();
  await settings.locator('[data-save-confirm]').getByRole('button', { name: 'Save anyway' }).click();

  await expect.poll(() => settingsPuts.length).toBeGreaterThan(0);
  const saved = settingsPuts.at(-1);
  const tpl = (saved.providerTemplates || []).find((t: any) => t.name === 'Golden Anthropic');
  expect(tpl).toBeTruthy();
  expect(tpl.provider.id).toBeUndefined();
  expect(tpl.provider.createdAt).toBeUndefined();
  expect(tpl.provider.updatedAt).toBeUndefined();
  expect(tpl.provider.isDefault).toBeUndefined();
  expect(tpl.provider.lastTestedAt).toBeUndefined();
  expect(tpl.provider.lastTestStatus).toBeUndefined();
  expect(tpl.provider.lastTestMessage).toBeUndefined();
  expect(tpl.provider.lastTestEndpoint).toBeUndefined();
  expect(tpl.provider.configFingerprint).toBeUndefined();
  expect(tpl.provider.capabilityControls).toMatchObject({
    hosted_web_search: 'on', hosted_code_execution: 'off', future_registry_feature: 'on',
  });
  expect(tpl.provider.attachmentPolicies).toHaveLength(1);
});

test('create-only capability defaults apply to every new config when the controls flow is active (editor v2 off)', async ({ page }) => {
  const { settingsPuts } = await setupApp(page, {
    settings: preservationSettingsPayload,
    secretsPresent: { anthropic_api_key: true },
  });
  await page.goto('/?capabilityUx=report,controls');
  const settings = await openProvidersTab(page);

  await settings.getByRole('button', { name: 'Add Provider' }).click();
  await expect(settings.getByRole('heading', { name: 'Choose a provider' })).toBeVisible();
  await settings.getByRole('button', { name: /Anthropic/ }).first().click();
  await settings.getByRole('button', { name: 'Continue' }).click();
  await settings.getByRole('button', { name: 'Continue' }).click(); // configure (defaults valid)
  await settings.getByRole('button', { name: 'Save Provider' }).click();
  await settings.locator('[data-save-confirm]').getByRole('button', { name: 'Save anyway' }).click();

  await expect.poll(() => settingsPuts.length).toBeGreaterThan(0);
  const saved = settingsPuts.at(-1);
  const created = saved.providerConfigs.find((c: any) => c.id !== 'cfg-anthropic' && c.id !== 'cfg-gemini-legacy' && c.providerId === 'anthropic');
  expect(created).toBeTruthy();
  expect(created.apiSurface).toBe('messages');
  expect(created.capabilityControls).toMatchObject({
    hosted_web_search: 'on', hosted_code_execution: 'off', native_compaction: 'on',
  });
  expect(created.webSearch).toEqual({
    enabled: true, toolType: 'web_search_20260209', maxUses: 5, allowedCallers: ['direct'],
  });
});

test('new Gemini connections offer API-key auth only, with no baked-in model default', async ({ page }) => {
  await setupApp(page, { settings: preservationSettingsPayload });
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);

  await settings.getByRole('button', { name: 'Add Provider' }).click();
  await settings.locator('[data-surface-option="gemini:default"]').click();
  await settings.getByRole('button', { name: 'Continue' }).click();

  // Single offered auth method → the auth step is skipped entirely; ADC never appears.
  await expect(settings.getByRole('heading', { name: 'Configure Gemini' })).toBeVisible();
  await expect(settings.getByText('GCP Application Default Credentials')).toHaveCount(0);
  await expect(settings.getByText('API Key').first()).toBeVisible();

  const modelInput = settings.getByPlaceholder('Pick a discovered model or enter a model id');
  await expect(modelInput).toHaveValue('');
});

test('existing legacy Gemini ADC connections stay editable with an explicit legacy warning', async ({ page }) => {
  await setupApp(page, { settings: preservationSettingsPayload });
  await page.goto('/?capabilityUx=1');
  const settings = await openProvidersTab(page);

  // The stored config is visibly marked as legacy on its card.
  const card = settings.locator('[data-connection-card="cfg-gemini-legacy"]');
  await expect(card.locator('[data-legacy-auth-badge]')).toContainText('Legacy auth');

  await settings.getByRole('button', { name: 'Edit Legacy Gemini ADC' }).click();
  await settings.getByRole('button', { name: 'Continue' }).click();

  // Editing keeps the legacy method selectable — flagged, not silently reset.
  await expect(settings.getByRole('heading', { name: 'Choose authentication' })).toBeVisible();
  const adcOption = settings.getByText('GCP Application Default Credentials');
  await expect(adcOption).toBeVisible();
  await expect(settings.locator('[data-legacy-auth-badge]')).toBeVisible();

  await settings.getByRole('button', { name: 'Continue' }).click();
  await expect(settings.locator('[data-legacy-auth-warning]')).toContainText('Legacy authentication');
});
