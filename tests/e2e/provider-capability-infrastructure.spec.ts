import { expect, test } from '@playwright/test';

const HARNESS = '/tests/e2e/fixtures/module-harness.html';

test('capability migration preserves legacy behavior and future fields', async ({ page }) => {
  await page.goto(HARNESS, { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const { DEFAULT_SETTINGS } = await import('/constants.ts' as string);
    const { migrateSettings } = await import('/services/settingsMigration.ts' as string);

    const raw = structuredClone(DEFAULT_SETTINGS) as Record<string, any>;
    raw.provider = { providerId: 'openai', model: 'gpt-test', profileId: null };
    raw.featureFlags.providers = {
      openai: true,
      anthropic: true,
      gemini: true,
      bedrock: true,
      compatible: true,
    };
    raw.providerConfigs = [
      {
        id: 'openai-legacy', displayName: 'OpenAI', providerId: 'openai',
        model: 'gpt-test', apiType: 'responses', compactionEnabled: undefined,
      },
      {
        id: 'anthropic-legacy', displayName: 'Anthropic', providerId: 'anthropic',
        model: 'claude-test', apiType: 'messages', webSearch: { enabled: false },
        codeExecution: { enabled: true }, compactionEnabled: false,
      },
      {
        id: 'bedrock-legacy', displayName: 'Bedrock', providerId: 'bedrock',
        model: 'claude-opus-4-6', apiType: 'messages',
      },
      {
        id: 'future', displayName: 'Future', providerId: 'compatible',
        model: 'future-model', apiType: 'responses',
        capabilityControls: {
          hosted_web_search: 'off', hosted_code_execution: 'off', future_registry_feature: 'on',
        },
        attachmentPolicies: [{
          id: 'policy-1', class: 'future_class', action: 'future_action',
          apiSurface: 'responses', createdAt: 123, futurePolicyField: { keep: true },
        }],
        futureConnectionField: { nested: ['keep-me'] },
      },
    ];
    raw.activeProviderConfigId = 'openai-legacy';

    const once = migrateSettings(raw);
    const twice = migrateSettings(JSON.parse(JSON.stringify(once)));
    const byId = Object.fromEntries((twice.providerConfigs || []).map((config: any) => [config.id, config]));
    return {
      openai: byId['openai-legacy'],
      anthropic: byId['anthropic-legacy'],
      bedrock: byId['bedrock-legacy'],
      future: byId.future,
      flags: twice.featureFlags?.capabilityUx,
    };
  });

  expect(result.openai.apiSurface).toBe('responses');
  expect(result.openai.capabilityControls).toMatchObject({
    hosted_web_search: 'on',
    hosted_code_execution: 'off',
    native_compaction: 'auto',
  });
  expect(result.anthropic.capabilityControls).toMatchObject({
    hosted_web_search: 'on',
    hosted_code_execution: 'on',
    native_compaction: 'off',
  });
  expect(result.anthropic.webSearch).toEqual({
    enabled: true, toolType: 'web_search_20260209', maxUses: 5, allowedCallers: ['direct'],
  });
  expect(result.bedrock.apiSurface).toBe('invoke_stream');
  expect(result.bedrock.model).toBe('anthropic.claude-opus-4-6-v1');
  expect(result.bedrock.capabilityControls).toMatchObject({
    hosted_web_search: 'off', hosted_code_execution: 'off',
  });
  expect(result.future.capabilityControls.future_registry_feature).toBe('on');
  expect(result.future.attachmentPolicies[0].futurePolicyField).toEqual({ keep: true });
  expect(result.future.futureConnectionField).toEqual({ nested: ['keep-me'] });
  expect(result.flags).toEqual({
    report: false, editorV2: false, controls: false, preflight: false, composerTarget: false,
  });
});

test('new connection defaults keep explicit-consent capabilities off', async ({ page }) => {
  await page.goto(HARNESS, { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const { DEFAULT_SETTINGS } = await import('/constants.ts' as string);
    const { createNewConnectionCapabilityControls } = await import('/lib/capability/providerConfig.ts' as string);
    const { migrateSettings } = await import('/services/settingsMigration.ts' as string);
    const raw = structuredClone(DEFAULT_SETTINGS) as Record<string, any>;
    raw.provider = { providerId: 'openai', model: 'gpt-new', profileId: null };
    raw.featureFlags.providers.openai = true;
    raw.providerConfigs = [{
      id: 'new-openai', displayName: 'New OpenAI', providerId: 'openai', model: 'gpt-new',
      apiType: 'responses', capabilityControls: createNewConnectionCapabilityControls(),
    }];
    raw.activeProviderConfigId = 'new-openai';
    const migrated = migrateSettings(raw);
    return migrated.providerConfigs?.[0];
  });

  expect(result?.capabilityControls).toMatchObject({
    hosted_web_search: 'off', hosted_code_execution: 'off', native_compaction: 'auto',
  });
  expect(result?.webSearch).toEqual({ enabled: false, toolType: 'web_search' });
});

test('capability service clients use the negotiated API contracts', async ({ page }) => {
  await page.goto(HARNESS, { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const capabilityApi = await import('/services/capabilityApi.ts' as string);
    const discoveryApi = await import('/services/modelDiscoveryApi.ts' as string);
    const checkApi = await import('/services/providerCheckApi.ts' as string);
    const calls: Array<{ url: string; method: string; body: any }> = [];
    const originalFetch = window.fetch;
    const report = {
      target: { connectionId: null, providerFamily: 'anthropic', apiSurface: 'messages' },
      fingerprint: 'fp', catalogVersion: 'v1', generatedAt: new Date(0).toISOString(), features: {},
    };

    window.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      calls.push({
        url,
        method: init?.method || 'GET',
        body: typeof init?.body === 'string' && init.body ? JSON.parse(init.body) : null,
      });
      let body: unknown = report;
      if (url.includes('/models/discover')) {
        body = { source: 'provider_api', fetchedAt: new Date(0).toISOString(), error: null, models: [{ id: 'm1' }] };
      } else if (url === '/api/providers/test') {
        body = { ok: true, fingerprint: 'fp', results: [{ probe: 'transport', verdict: 'verified' }] };
      } else if (url.includes('/runs/preflight')) {
        body = { planId: 'plan-1', requirementsHash: 'hash', status: 'clean', blockers: [], choices: [], notices: [], staleEvidence: false, plan: report };
      } else if (url.endsWith('/target')) {
        body = {};
      }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }) as typeof window.fetch;

    try {
      await capabilityApi.getConnectionCapabilityReport('connection / one', 'claude test');
      await capabilityApi.resolveCapabilityReport({
        id: 'draft', displayName: 'Draft', providerId: 'anthropic', model: 'claude-test',
      });
      await discoveryApi.discoverProviderModels({
        id: 'draft', displayName: 'Draft', providerId: 'anthropic', model: 'claude-test',
      });
      await checkApi.runProviderCheck({ connectionId: 'connection-1', tier: 'quick' });
      await capabilityApi.preflightThreadRun('thread-1', {
        target: { connectionId: 'connection-1', model: 'claude-test' },
        requirements: { attachments: [], features: ['custom_tools'] },
      });
      await capabilityApi.updateThreadTarget('thread-1', { connectionId: 'connection-1', model: 'claude-test' });
    } finally {
      window.fetch = originalFetch;
    }
    return calls;
  });

  expect(result.map((call) => [call.method, call.url])).toEqual([
    ['GET', '/api/providers/connections/connection%20%2F%20one/capabilities?model=claude+test'],
    ['POST', '/api/providers/capabilities/resolve'],
    ['POST', '/api/providers/models/discover'],
    ['POST', '/api/providers/test'],
    ['POST', '/api/threads/thread-1/runs/preflight'],
    ['PUT', '/api/threads/thread-1/target'],
  ]);
  expect(result[1].body.model).toBe('claude-test');
  expect(result[4].body.requirements.features).toEqual(['custom_tools']);
});

test('unknown capability semantics fail closed instead of rendering green', async ({ page }) => {
  await page.goto(HARNESS, { waitUntil: 'domcontentloaded' });
  const result = await page.evaluate(async () => {
    const format = await import('/lib/capability/format.ts' as string);
    const base = {
      meta: { label: 'Future feature', group: 'future_group', headline: false, order: 1 },
      class: 'future_class', riskTags: [], state: 'future_state', mode: null,
      effective: 'future_effective', control: null, permission: null,
      conditions: [], reasons: [], limits: null, detail: { future: true }, evidence: [],
    };
    return {
      state: format.capabilityDisplayState(base),
      sorted: format.sortedCapabilityEntries({ z_future: base, a_future: { ...base, meta: { ...base.meta, order: 0 } } }).map(([key]: [string]) => key),
    };
  });

  expect(result.state).toBe('unverified');
  expect(result.sorted).toEqual(['a_future', 'z_future']);
});

test('persisted run-plan DTO parses exactly: wrapped envelope, plan report, selection array, epoch times', async ({ page }) => {
  await page.goto(HARNESS, { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const api = await import('/services/capabilityApi.ts' as string);
    const report = {
      target: {
        connectionId: 'cfg-1', providerFamily: 'bedrock', apiSurface: 'converse_stream',
        endpointFamily: 'bedrock-runtime', model: 'anthropic.claude-opus-4-6-v1',
        region: 'us-east-1', adapterVersion: 'v1',
      },
      fingerprint: 'fp', catalogVersion: 'v1', generatedAt: new Date(0).toISOString(), features: {},
    };
    const persistedDto = {
      planId: 'plan-9',
      requirementsHash: 'hash-9',
      status: 'choices_required',
      blockers: [],
      choices: [{
        issueId: 'issue-1', code: 'fidelity_reduction', text: 'report.docx can’t be sent natively.',
        feature: null, attachmentId: 'file-1', fix: null,
        options: [{
          id: 'converse', label: 'Switch to Converse', consequence: 'Native compaction is skipped',
          policyClass: null, apiSurface: 'converse_stream',
        }],
        allowedScopes: ['run'],
      }],
      notices: [],
      staleEvidence: false,
      plan: report,
      expiresAt: 1893456000000,
      lifecycleStatus: 'finalized',
      target: { connectionId: 'cfg-1', model: 'anthropic.claude-opus-4-6-v1' },
      choiceSelections: [{
        issueId: 'issue-1', optionId: 'converse', scope: 'run', selectedAt: '2026-07-11T00:00:00Z',
      }],
      createdAt: 1752192000000,
      finalizedAt: 1752192001000,
      runId: 'run-9',
    };

    const originalFetch = window.fetch;
    const results: any[] = [];
    try {
      // Backend shape today: the DTO wrapped in a {plan: …} envelope.
      window.fetch = (async () => new Response(JSON.stringify({ plan: persistedDto }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })) as typeof window.fetch;
      results.push(await api.getRunCapabilityPlan('run-9'));

      // Forward compatibility: an unwrapped DTO must not be mistaken for an
      // envelope (its own `plan` field is the CapabilityReport, not the DTO).
      window.fetch = (async () => new Response(JSON.stringify(persistedDto), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })) as typeof window.fetch;
      results.push(await api.getRunCapabilityPlan('run-9'));
    } finally {
      window.fetch = originalFetch;
    }
    return results;
  });

  for (const record of result) {
    expect(record.planId).toBe('plan-9');
    expect(record.requirementsHash).toBe('hash-9');
    expect(record.status).toBe('choices_required');
    expect(record.lifecycleStatus).toBe('finalized');
    expect(record.staleEvidence).toBe(false);
    // `plan` is the CapabilityReport — never the persisted DTO itself.
    expect(record.plan.target.apiSurface).toBe('converse_stream');
    expect(record.target).toEqual({ connectionId: 'cfg-1', model: 'anthropic.claude-opus-4-6-v1' });
    expect(Array.isArray(record.choiceSelections)).toBe(true);
    expect(record.choiceSelections[0]).toMatchObject({
      issueId: 'issue-1', optionId: 'converse', scope: 'run', selectedAt: '2026-07-11T00:00:00Z',
    });
    expect(record.choices[0].options[0].apiSurface).toBe('converse_stream');
    expect(record.expiresAt).toBe(1893456000000);
    expect(record.createdAt).toBe(1752192000000);
    expect(record.finalizedAt).toBe(1752192001000);
    expect(record.runId).toBe('run-9');
  }
});

test('the safe probe vocabulary includes the strict system_instructions probe', async ({ page }) => {
  await page.goto(HARNESS, { waitUntil: 'domcontentloaded' });
  const result = await page.evaluate(async () => {
    const types = await import('/lib/capability/types.ts' as string);
    const panel = await import('/components/settings/providers/CapabilityCheckPanel.tsx' as string);
    return {
      v1Probes: types.V1_CAPABILITY_PROBES,
      safeProbeKeys: panel.SAFE_CAPABILITY_PROBES.map((p: any) => p.key),
    };
  });

  expect(result.v1Probes).toContain('system_instructions');
  expect(result.safeProbeKeys).toContain('system_instructions');
});

test('capability API errors retain HTTP status and machine-readable code', async ({ page }) => {
  await page.goto(HARNESS, { waitUntil: 'domcontentloaded' });
  const error = await page.evaluate(async () => {
    const api = await import('/services/capabilityApi.ts' as string);
    const originalFetch = window.fetch;
    window.fetch = (async () => new Response(JSON.stringify({
      error: { code: 'plan_stale', message: 'The saved plan expired.' },
    }), { status: 409, headers: { 'Content-Type': 'application/json' } })) as typeof window.fetch;
    try {
      await api.preflightThreadRun('thread-1', {
        target: { connectionId: 'connection-1', model: 'model-1' },
        requirements: { attachments: [], features: [] },
      });
      return null;
    } catch (cause: any) {
      return { name: cause.name, status: cause.status, code: cause.code, message: cause.message };
    } finally {
      window.fetch = originalFetch;
    }
  });

  expect(error).toEqual({
    name: 'CapabilityApiError',
    status: 409,
    code: 'plan_stale',
    message: 'The saved plan expired.',
  });
});
