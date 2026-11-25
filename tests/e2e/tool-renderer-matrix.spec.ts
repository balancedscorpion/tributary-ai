import { expect, test } from '@playwright/test';

test('generated tool metadata resolves through migrated native renderer paths', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const renderers = await import('/components/renderers/index.ts' as string);
    const generated = await import('/lib/generatedToolMetadata.ts' as string);
    const tools = Array.from(generated.GENERATED_STATIC_TOOL_METADATA as any[]);

    const failures: Array<{ name: string; combo: string; label: string | null }> = [];
    const comboLabels = new Map<string, Set<string>>();
    const catalogLabels: Record<string, string | null> = {};

    for (const meta of tools) {
      const presentation = meta.presentation || {};
      const combo = `${presentation.native_viewer || ''}/${presentation.renderer_strategy || ''}`;
      const artifact = {
        id: `renderer-${meta.name}`,
        // Use image deliberately. Every migrated tool should be claimed by
        // tool_name, custom resolver, or presentation before plain type fallback.
        type: 'image',
        title: meta.label || meta.name,
        content: '[]',
        metadata: {
          ok: true,
          tool_name: meta.name,
          tool_args: {},
          tool_presentation: presentation,
          result_metadata: {
            tool_name: meta.name,
            tool_presentation: presentation,
            integration: meta.connector_id || presentation.family,
          },
        },
      };
      const entry = renderers.resolveRenderer(artifact as any);
      const label = entry?.label || null;
      if (!comboLabels.has(combo)) comboLabels.set(combo, new Set());
      if (label) comboLabels.get(combo)!.add(label);
      if (!label || label === 'Image') {
        failures.push({ name: meta.name, combo, label });
      }
      if (presentation.renderer_strategy === 'tool') {
        catalogLabels[meta.name] = label;
      }
    }

    return {
      failures,
      comboLabels: Object.fromEntries(
        Array.from(comboLabels.entries()).map(([combo, labels]) => [combo, Array.from(labels).sort()])
      ),
      catalogLabels,
    };
  });

  expect(result.failures).toEqual([]);
  expect(result.comboLabels['connector_inspector/tool']).toEqual(['Catalog']);
  expect(Object.values(result.catalogLabels)).toHaveLength(15);
  for (const [toolName, label] of Object.entries(result.catalogLabels)) {
    expect(label, `${toolName} should resolve through CatalogBrowserRenderer`).toBe('Catalog');
  }
  for (const combo of [
    'chart/artifact_type',
    'code/artifact_type',
    'connector_inspector/family',
    'data/artifact_type',
    'file/artifact_type',
    'interactive_html/artifact_type',
    'query/artifact_type',
  ]) {
    expect(result.comboLabels[combo]?.length, `${combo} should have runtime renderer coverage`).toBeGreaterThan(0);
  }
});

test('generated tool metadata survives frontend inventory normalization without drift', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const generated = await import('/lib/generatedToolMetadata.ts' as string);
    const inventoryModule = await import('/lib/toolInventory.ts' as string);
    const generatedTools = Array.from(generated.GENERATED_STATIC_TOOL_METADATA as any[]);
    const hosted = inventoryModule.staticProviderHostedToolCapabilitiesForFixture();
    const inventory = inventoryModule.tryNormalizeRuntimeToolInventory(generatedTools, hosted);
    if (!inventory) return { missingInventory: true };

    const normalizedTools: any[] = [];
    for (const tools of Object.values(inventory.connectionTools || {}) as any[][]) {
      normalizedTools.push(...tools);
    }
    for (const group of inventory.appToolGroups || []) {
      normalizedTools.push(...(group.tools || []));
    }
    const registeredNormalizedTools = normalizedTools.filter(
      (tool) => tool.origin !== 'provider' && tool.toolSetId !== 'provider-hosted',
    );

    const normalizedByName = new Map(registeredNormalizedTools.map((tool) => [tool.name, tool]));
    const missing: string[] = [];
    const stale: string[] = [];
    const fieldDrift: string[] = [];
    const generatedNames = new Set(generatedTools.map((tool) => tool.name));

    for (const meta of generatedTools) {
      const tool = normalizedByName.get(meta.name);
      if (!tool) {
        missing.push(meta.name);
        continue;
      }
      const checks: Array<[string, unknown, unknown]> = [
        ['category', tool.category, meta.category],
        ['origin', tool.origin, meta.origin],
        ['toolSetId', tool.toolSetId, meta.tool_set_id],
        ['toolSetName', tool.toolSetName, meta.tool_set_name],
        ['connectorId', tool.connectorId || '', meta.connector_id || ''],
        ['presentation', tool.presentation, meta.presentation],
        ['test', tool.test, meta.test],
      ];
      for (const [field, got, want] of checks) {
        if (JSON.stringify(got ?? null) !== JSON.stringify(want ?? null)) {
          fieldDrift.push(`${meta.name}.${field}`);
        }
      }
    }

    for (const tool of registeredNormalizedTools) {
      if (!generatedNames.has(tool.name)) stale.push(tool.name);
    }

    return {
      source: inventory.source,
      generatedCount: generatedTools.length,
      normalizedCount: registeredNormalizedTools.length,
      providerHostedCount: normalizedTools.length - registeredNormalizedTools.length,
      missing: missing.sort(),
      stale: stale.sort(),
      fieldDrift: fieldDrift.sort(),
    };
  });

  expect(result.source).toBe('runtime');
  expect(result.generatedCount).toBeGreaterThan(0);
  expect(result.normalizedCount).toBe(result.generatedCount);
  expect(result.providerHostedCount).toBe(4);
  expect(result.missing).toEqual([]);
  expect(result.stale).toEqual([]);
  expect(result.fieldDrift).toEqual([]);
});

test('provider-hosted capabilities normalize into distinct permission rows', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const generated = await import('/lib/generatedToolMetadata.ts' as string);
    const inventoryModule = await import('/lib/toolInventory.ts' as string);
    const generatedTools = Array.from(generated.GENERATED_STATIC_TOOL_METADATA as any[]);
    const hosted = inventoryModule.staticProviderHostedToolCapabilitiesForFixture();

    const inventory = inventoryModule.tryNormalizeRuntimeToolInventory(generatedTools, hosted);
    const group = inventory?.appToolGroups.find((candidate: any) => candidate.id === 'provider-hosted');
    return {
      source: inventory?.source,
      title: group?.title,
      names: group?.tools.map((tool: any) => tool.name),
      subjectKinds: group?.tools.map((tool: any) => tool.permissionSubjectKind),
      operations: group?.tools.map((tool: any) => tool.permissionOperation),
      connectorIds: group?.tools.map((tool: any) => tool.connectorId),
      testModes: group?.tools.map((tool: any) => tool.test?.mode),
    };
  });

  expect(result).toEqual({
    source: 'runtime',
    title: 'Model provider capabilities',
    names: [
      'anthropic-messages/code_execution',
      'anthropic-messages/web_search',
      'openai-responses/code_interpreter',
      'openai-responses/web_search',
    ],
    subjectKinds: [
      'provider_hosted_tool',
      'provider_hosted_tool',
      'provider_hosted_tool',
      'provider_hosted_tool',
    ],
    operations: ['codeExecution', 'webSearch', 'codeExecution', 'webSearch'],
    connectorIds: [
      'anthropic-messages',
      'anthropic-messages',
      'openai-responses',
      'openai-responses',
    ],
    testModes: ['connection_check', 'connection_check', 'connection_check', 'connection_check'],
  });
});

test('empty provider-hosted runtime capabilities are not replaced with static capabilities', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const generated = await import('/lib/generatedToolMetadata.ts' as string);
    const inventoryModule = await import('/lib/toolInventory.ts' as string);
    const generatedTools = Array.from(generated.GENERATED_STATIC_TOOL_METADATA as any[]);
    const inventory = inventoryModule.tryNormalizeRuntimeToolInventory(generatedTools, []);
    const group = inventory?.appToolGroups.find((candidate: any) => candidate.id === 'provider-hosted');
    return {
      source: inventory?.source,
      names: group?.tools.map((tool: any) => tool.name) ?? [],
    };
  });

  expect(result).toEqual({
    source: 'runtime',
    names: [],
  });
});

test('inventory service accepts an explicit empty provider-hosted runtime list', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const generated = await import('/lib/generatedToolMetadata.ts' as string);
    const service = await import('/services/toolInventoryApi.ts' as string);
    const originalFetch = window.fetch;
    window.fetch = async () =>
      new Response(
        JSON.stringify({
          tools: Array.from(generated.GENERATED_STATIC_TOOL_METADATA as any[]),
          provider_hosted_tools: [],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      );
    try {
      const inventory = await service.fetchToolInventory();
      const group = inventory.appToolGroups.find((candidate: any) => candidate.id === 'provider-hosted');
      return {
        source: inventory.source,
        providerHostedNames: group?.tools.map((tool: any) => tool.name) ?? [],
      };
    } finally {
      window.fetch = originalFetch;
    }
  });

  expect(result).toEqual({
    source: 'runtime',
    providerHostedNames: [],
  });
});

test('tool-scoped runtime permission matching preserves connector provenance', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const inventoryModule = await import('/lib/toolInventory.ts' as string);
    const alpha = {
      name: 'shared_runtime_search',
      label: 'Shared runtime search',
      category: 'read',
      origin: 'connected',
      toolSetId: 'alpha-runtime',
      toolSetName: 'Alpha Runtime',
      connectorId: 'alpha-runtime',
    };
    const beta = {
      ...alpha,
      toolSetId: 'beta-runtime',
      toolSetName: 'Beta Runtime',
      connectorId: 'beta-runtime',
    };
    const rules = [
      {
        id: 'alpha-off',
        category: 'read',
        effect: 'deny',
        scope: 'global',
        subject: {
          kind: 'tool',
          tool: 'shared_runtime_search',
          connector: 'alpha-runtime',
          tool_set_id: 'alpha-runtime',
        },
      },
    ];
    return {
      alpha: inventoryModule.resolveStandaloneToolPermission(alpha, rules).effect,
      beta: inventoryModule.resolveStandaloneToolPermission(beta, rules).effect,
    };
  });

  expect(result).toEqual({
    alpha: 'deny',
    beta: 'allow',
  });
});

test('provider-hosted capabilities remain visible for older runtime inventory responses', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const generated = await import('/lib/generatedToolMetadata.ts' as string);
    const inventoryModule = await import('/lib/toolInventory.ts' as string);
    const generatedTools = Array.from(generated.GENERATED_STATIC_TOOL_METADATA as any[]);
    const inventory = inventoryModule.tryNormalizeRuntimeToolInventory(generatedTools);
    const group = inventory?.appToolGroups.find((candidate: any) => candidate.id === 'provider-hosted');
    return {
      source: inventory?.source,
      origin: group?.origin,
      names: group?.tools.map((tool: any) => tool.name),
      subjectKinds: group?.tools.map((tool: any) => tool.permissionSubjectKind),
    };
  });

  expect(result).toEqual({
    source: 'runtime',
    origin: 'provider',
    names: [
      'anthropic-messages/code_execution',
      'anthropic-messages/web_search',
      'openai-responses/code_interpreter',
      'openai-responses/web_search',
    ],
    subjectKinds: [
      'provider_hosted_tool',
      'provider_hosted_tool',
      'provider_hosted_tool',
      'provider_hosted_tool',
    ],
  });
});

test('provider-hosted tool-set permissions stay scoped to the adapter family', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const generated = await import('/lib/generatedToolMetadata.ts' as string);
    const inventoryModule = await import('/lib/toolInventory.ts' as string);
    const generatedTools = Array.from(generated.GENERATED_STATIC_TOOL_METADATA as any[]);
    const inventory = inventoryModule.tryNormalizeRuntimeToolInventory(generatedTools);
    const group = inventory?.appToolGroups.find((candidate: any) => candidate.id === 'provider-hosted');
    const openAICode = group?.tools.find((tool: any) => tool.name === 'openai-responses/code_interpreter');
    const anthropicCode = group?.tools.find((tool: any) => tool.name === 'anthropic-messages/code_execution');
    const rules = [
      {
        id: 'openai-code-always',
        category: 'execute',
        effect: 'allow',
        scope: 'global',
        subject: {
          kind: 'tool_set',
          tool_set_id: 'provider-hosted',
          connector: 'openai-responses',
        },
      },
    ];
    return {
      openAI: openAICode
        ? inventoryModule.resolveStandaloneToolPermission(openAICode, rules).effect
        : null,
      anthropic: anthropicCode
        ? inventoryModule.resolveStandaloneToolPermission(anthropicCode, rules).effect
        : null,
    };
  });

  expect(result).toEqual({
    openAI: 'allow',
    anthropic: 'ask',
  });
});
