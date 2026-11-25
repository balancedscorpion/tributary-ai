import { expect, test, type Page, type Route } from '@playwright/test';

/**
 * Settings-side capability interactions (plan §4.3/§4.7 with the `controls`
 * flag): interactive standard vs explicit-consent controls, capability checks
 * with honest verdicts + report refresh, and attachment-policy revocation.
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

const connectionReport = {
  target: {
    connectionId: 'cfg-anthropic', providerFamily: 'anthropic', apiSurface: 'messages',
    endpointFamily: 'anthropic-api', model: 'claude-opus-4-6', region: null, adapterVersion: 'test',
  },
  fingerprint: 'fp-1',
  catalogVersion: 'catalog-1',
  generatedAt: new Date().toISOString(),
  features: {
    native_compaction: feature({
      meta: { label: 'Native compaction', group: 'state', headline: false, order: 0 },
      state: 'supported', effective: 'on',
      control: { setting: 'auto', controllable: true },
    }),
    hosted_web_search: feature({
      meta: { label: 'Web search', group: 'hosted', headline: true, order: 0 },
      class: 'explicit_consent', riskTags: ['billable', 'external_egress'],
      state: 'supported', effective: 'off',
      control: { setting: 'off', controllable: true },
      permission: { required: true, granted: true, policyRef: null },
    }),
  },
};

const settingsPayload = {
  settings: {
    provider: { providerId: 'anthropic', model: 'claude-opus-4-6' },
    compatibleProfiles: [],
    featureFlags: {
      providers: { openai: false, anthropic: true, gemini: false, bedrock: true, compatible: true },
    },
    providerConfigs: [
      {
        id: 'cfg-anthropic',
        displayName: 'Work Anthropic',
        providerId: 'anthropic',
        model: 'claude-opus-4-6',
        authType: 'api_key',
        isDefault: true,
        capabilityControls: { hosted_web_search: 'off', hosted_code_execution: 'off', native_compaction: 'auto' },
        attachmentPolicies: [
          { id: 'pol-1', class: 'office_docs_to_text', action: 'extracted_text', apiSurface: 'messages', createdAt: Date.now() - 86_400_000 },
        ],
      },
    ],
    activeProviderConfigId: 'cfg-anthropic',
  },
};

interface Captured {
  settingsPuts: any[];
  checkBodies: any[];
  capabilityGets: number;
}

async function setupApp(page: Page, opts?: {
  checkResult?: unknown;
}): Promise<Captured> {
  const captured: Captured = { settingsPuts: [], checkBodies: [], capabilityGets: 0 };

  await page.route('**/api/**', async (route: Route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    const method = request.method();
    const postJson = () => {
      try { return JSON.parse(request.postData() || '{}'); } catch { return {}; }
    };

    if (pathname === '/api/settings' && method === 'GET') {
      await route.fulfill(jsonResponse(settingsPayload));
      return;
    }
    if (pathname === '/api/settings' && (method === 'PUT' || method === 'POST')) {
      captured.settingsPuts.push(postJson());
      await route.fulfill(jsonResponse({ settings: postJson() }));
      return;
    }
    if (pathname === '/api/activation/status') {
      await route.fulfill(jsonResponse({ activated: true }));
      return;
    }
    if (pathname === '/api/secrets/status') {
      await route.fulfill(jsonResponse({ initialized: true, locked: false, configured: {}, present: {} }));
      return;
    }
    if (pathname === '/api/threads' && method === 'GET') {
      await route.fulfill(jsonResponse({ threads: [] }));
      return;
    }
    if (/^\/api\/providers\/connections\/[^/]+\/capabilities$/.test(pathname) && method === 'GET') {
      captured.capabilityGets += 1;
      await route.fulfill(jsonResponse({ report: connectionReport }));
      return;
    }
    if (pathname === '/api/providers/capabilities/resolve' && method === 'POST') {
      await route.fulfill(jsonResponse({ report: connectionReport }));
      return;
    }
    if (pathname === '/api/providers/test' && method === 'POST') {
      captured.checkBodies.push(postJson());
      await route.fulfill(jsonResponse(opts?.checkResult ?? {
        ok: true,
        fingerprint: 'fp-1',
        results: [
          { probe: 'text_streaming', verdict: 'verified', reason: null, message: 'Streamed 12 tokens.', requestId: 'req-1', durationMs: 420 },
          { probe: 'system_instructions', verdict: 'verified', reason: null, message: 'Strict system-instruction probe honored.', requestId: 'req-1b', durationMs: 380 },
          { probe: 'custom_tools', verdict: 'verified', reason: null, message: 'Two-step tool loop completed.', requestId: 'req-2', durationMs: 900 },
          { probe: 'images', verdict: 'unsupported', reason: 'rejected_by_provider', message: 'The provider rejected image input.', requestId: 'req-3', durationMs: 300 },
          { probe: 'documents', verdict: 'failed', reason: 'timeout', message: 'The probe timed out.', requestId: null, durationMs: 30000 },
          { probe: 'structured_output', verdict: 'skipped', reason: 'probe_not_implemented', message: 'Not yet implemented server-side.', requestId: null, durationMs: 0 },
        ],
      }));
      return;
    }
    if (pathname === '/api/providers/models/discover') {
      await route.fulfill(jsonResponse({ error: 'not found' }, 404));
      return;
    }
    await route.fulfill(jsonResponse({}));
  });

  return captured;
}

async function openConnectionDetail(page: Page) {
  await page.getByRole('button', { name: 'Open settings' }).click();
  const settings = page.getByRole('dialog', { name: 'Settings' });
  await settings.getByRole('tab', { name: 'Providers' }).click();
  await settings.locator('[data-connection-card="cfg-anthropic"] [data-connection-chip-strip]').click();
  const detail = settings.locator('[data-connection-detail="cfg-anthropic"]');
  await expect(detail).toBeVisible();
  return { settings, detail };
}

test('provider details keep standard controls while hosted tools are managed as Connections', async ({ page }) => {
  const captured = await setupApp(page);
  await page.goto('/?capabilityUx=report,controls');
  const { detail } = await openConnectionDetail(page);

  // Standard (Class S) control exposes Auto.
  await detail.locator('[data-capability-control="native_compaction"]').click();
  await expect(page.getByRole('option', { name: 'Auto' })).toBeVisible();
  await page.keyboard.press('Escape');

  // Hosted consent capabilities remain visible as evidence, but configuration
  // moved to the Connections tab so Providers no longer offers a second toggle.
  const webRow = detail.locator('[data-capability-row="hosted_web_search"]');
  await expect(webRow.getByText(/Requires explicit enable and permission/)).toBeAttached();
  await expect(detail.locator('[data-capability-control="hosted_web_search"]')).toHaveCount(0);

  const settings = page.getByRole('dialog', { name: 'Settings' });
  await settings.getByRole('tab', { name: 'Connections' }).click();
  await settings.getByRole('button', { name: 'Add connection' }).click();
  await settings.getByRole('button', { name: /Web Search Provider-hosted search/ }).click();
  const webConnection = settings.getByRole('article').filter({ hasText: 'Web Search' });
  const webToggle = webConnection.getByRole('checkbox');
  await webToggle.check();

  // Web Search is one global model-attached connection. It enables directly;
  // there is no second provider picker or raw provider-tool configuration.
  await expect(settings.getByRole('dialog', { name: 'Set up connection — Web Search' })).toHaveCount(0);
  await expect.poll(() => captured.settingsPuts.length).toBeGreaterThan(0);
  const saved = captured.settingsPuts.at(-1);
  const savedCfg = saved.providerConfigs.find((c: any) => c.id === 'cfg-anthropic');
  expect(savedCfg.capabilityControls.hosted_web_search).toBe('on');
  expect(savedCfg.webSearch).toEqual({ enabled: true, toolType: 'web_search_20260209', maxUses: 5, allowedCallers: ['direct'] });
});

test('capability checks render honest verdicts and refresh the report', async ({ page }) => {
  const captured = await setupApp(page);
  await page.goto('/?capabilityUx=report,controls');
  const { detail } = await openConnectionDetail(page);
  const getsBeforeCheck = captured.capabilityGets;

  await detail.getByRole('button', { name: 'Run capability checks' }).click();
  await detail.getByRole('button', { name: /Run 6 checks/ }).click();

  const results = detail.locator('[data-capability-check-results]');
  await expect(results).toBeVisible();
  await expect(results.locator('[data-capability-check-result="text_streaming:verified"]')).toContainText('Verified');
  await expect(results.locator('[data-capability-check-result="system_instructions:verified"]')).toContainText('Verified');
  await expect(results.locator('[data-capability-check-result="images:unsupported"]')).toContainText('Unsupported');
  await expect(results.locator('[data-capability-check-result="documents:failed"]')).toContainText('Failed');
  const skipped = results.locator('[data-capability-check-result="structured_output:skipped"]');
  await expect(skipped).toContainText('Skipped');
  // Skipped is never green.
  await expect(skipped.locator('.text-emerald-400')).toHaveCount(0);

  expect(captured.checkBodies[0].tier).toBe('capability');
  expect(captured.checkBodies[0].probes).toEqual([
    'text_streaming', 'system_instructions', 'custom_tools', 'images', 'documents', 'structured_output',
  ]);
  // Consent-class hosted probes are absent from the safe bundle.
  expect(captured.checkBodies[0].probes).not.toContain('hosted_web_search');
  // A conclusive check invalidates and refetches the connection report.
  await expect.poll(() => captured.capabilityGets).toBeGreaterThan(getsBeforeCheck);
});

test('attachment policies list and revoke without window.confirm', async ({ page }) => {
  const captured = await setupApp(page);
  page.on('dialog', () => { throw new Error('window.confirm/prompt must not be used'); });
  await page.goto('/?capabilityUx=report,controls');
  const { detail } = await openConnectionDetail(page);

  const policy = detail.locator('[data-attachment-policy="pol-1"]');
  await expect(policy).toContainText('Office docs to text');
  await policy.getByRole('button', { name: /Remove Office docs to text policy/ }).click();
  await policy.getByRole('button', { name: 'Remove policy' }).click();

  await expect.poll(() => captured.settingsPuts.length).toBeGreaterThan(0);
  const saved = captured.settingsPuts.at(-1);
  const savedCfg = saved.providerConfigs.find((c: any) => c.id === 'cfg-anthropic');
  expect(savedCfg.attachmentPolicies).toEqual([]);
  await expect(detail.locator('[data-attachment-policies-empty]')).toBeVisible();
});
