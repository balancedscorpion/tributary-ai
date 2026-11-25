import { expect, test } from '@playwright/test';

/**
 * Pure-function contracts of the capability settings UI vocabulary:
 * state derivation (plan §3.3) and surface identity labels (plan §4.1/§4.2).
 * Runs through the module harness like provider-endpoint-preview.spec.ts.
 */

test('capabilityViewState maps resolutions to the shared state vocabulary', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const mod = await import('/components/settings/providers/CapabilityChip.tsx' as string);
    const view = (state: string, effective: string) => mod.capabilityViewState({ state, effective });
    return {
      unknownIsUnverified: view('unknown', 'on'),
      unsupportedWins: view('unsupported', 'on'),
      blocked: view('supported', 'blocked'),
      on: view('supported', 'on'),
      conditionalOn: view('conditional', 'on'),
      off: view('supported', 'off'),
      conditionalOff: view('conditional', 'off'),
      unavailableEffective: view('supported', 'unavailable'),
      words: {
        unverified: mod.CAPABILITY_STATE_META.unverified.word,
        not_available: mod.CAPABILITY_STATE_META.not_available.word,
        blocked: mod.CAPABILITY_STATE_META.blocked.word,
      },
      unverifiedClass: mod.CAPABILITY_STATE_META.unverified.className,
      onClass: mod.CAPABILITY_STATE_META.on.className,
      modifiers: {
        emulated: mod.modeModifierText('emulated'),
        reduced: mod.modeModifierText('reduced'),
        native: mod.modeModifierText('native'),
      },
    };
  });

  expect(result.unknownIsUnverified).toBe('unverified');
  expect(result.unsupportedWins).toBe('not_available');
  expect(result.blocked).toBe('blocked');
  expect(result.on).toBe('on');
  expect(result.conditionalOn).toBe('conditional');
  expect(result.off).toBe('off');
  expect(result.conditionalOff).toBe('off');
  expect(result.unavailableEffective).toBe('not_available');
  expect(result.words).toEqual({ unverified: 'Unverified', not_available: 'Not available', blocked: 'Blocked' });
  // Unknown must never look successful: unverified renders muted, distinct from the success class.
  expect(result.unverifiedClass).not.toBe(result.onClass);
  expect(result.unverifiedClass).toContain('muted');
  expect(result.modifiers).toEqual({ emulated: '≈ emulated', reduced: '▽ reduced fidelity', native: null });
});

test('surface identity labels separate family from exact API surface', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const mod = await import('/components/settings/providers/surfaceIdentity.ts' as string);
    return {
      anthropic: mod.describeSurface('anthropic', undefined),
      bedrockMessages: mod.describeSurface('bedrock', 'messages'),
      bedrockChat: mod.describeSurface('bedrock', 'chat_completions'),
      bedrockResponses: mod.describeSurface('bedrock', 'responses'),
      compatibleResponses: mod.describeSurface('compatible', 'responses'),
      identityLine: mod.connectionIdentityLine({ providerId: 'bedrock', apiType: 'messages', model: 'anthropic.claude-opus-4-6-v1' }),
    };
  });

  expect(result.anthropic.familyLabel).toBe('Anthropic');
  expect(result.anthropic.surfaceLabel).toBe('Claude API · Messages');
  expect(result.bedrockMessages.familyLabel).toBe('Amazon Bedrock');
  expect(result.bedrockMessages.surfaceLabel).toBe('Claude · Messages');
  expect(result.bedrockChat.surfaceLabel).toBe('Chat Completions');
  expect(result.bedrockResponses.surfaceLabel).toBe('Responses (Mantle)');
  expect(result.compatibleResponses.familyLabel).toBe('OpenAI-compatible');
  expect(result.identityLine).toEqual(['Amazon Bedrock', 'Claude · Messages', 'anthropic.claude-opus-4-6-v1']);
});

test('surface catalog uses canonical backend endpoint families — no google-ai/custom aliases', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const mod = await import('/providers/catalog/providerSurfaces.ts' as string);
    return {
      gemini: mod.deriveProviderSurface({ providerId: 'gemini' }).endpointFamily,
      compatibleChat: mod.deriveProviderSurface({ providerId: 'compatible', apiType: 'chat_completions' }).endpointFamily,
      compatibleResponses: mod.deriveProviderSurface({ providerId: 'compatible', apiType: 'responses' }).endpointFamily,
      allFamilies: mod.PROVIDER_SURFACES.map((s: any) => s.endpointFamily),
    };
  });

  expect(result.gemini).toBe('gemini-developer');
  expect(result.compatibleChat).toBe('compatible-custom');
  expect(result.compatibleResponses).toBe('compatible-custom');
  expect(result.allFamilies).not.toContain('google-ai');
  expect(result.allFamilies).not.toContain('custom');
});

test('plan-update vocabulary accepts canonical continuation_loss and legacy continuation_lost', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const mod = await import('/components/chat/PlanUpdateRow.tsx' as string);
    return {
      canonical: mod.planUpdateKindLabel('continuation_loss'),
      legacy: mod.planUpdateKindLabel('continuation_lost'),
      surfaceSwitch: mod.planUpdateKindLabel('surface_switch'),
      unknownFallsBack: mod.planUpdateKindLabel('future_change_kind'),
      emptyFallsBack: mod.planUpdateKindLabel(null),
    };
  });

  expect(result.canonical).toBe('Continuation not carried over');
  expect(result.legacy).toBe(result.canonical);
  expect(result.surfaceSwitch).toBe('API surface changed');
  expect(result.unknownFallsBack).toBe('Plan update');
  expect(result.emptyFallsBack).toBe('Plan update');
});

test('targetIdentityLabel lives in the catalog, not a React component', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const catalog = await import('/providers/catalog/targetIdentity.ts' as string);
    const picker = await import('/components/chat/TargetPicker.tsx' as string);
    return {
      label: catalog.targetIdentityLabel({
        id: 'cfg-1', displayName: 'Bedrock', providerId: 'bedrock',
        model: 'anthropic.claude-opus-4-6-v1', apiType: 'messages',
      }),
      // The data-layer helper must no longer be re-exported by the UI module.
      pickerExportsLabel: typeof (picker as any).targetIdentityLabel,
    };
  });

  expect(result.label).toBe('Amazon Bedrock · Claude · Messages · anthropic.claude-opus-4-6-v1');
  expect(result.pickerExportsLabel).toBe('undefined');
});

test('capability flags: URL comma-list is the complete effective set and composerTarget requires preflight', async ({ page }) => {
  // Param list present: persisted flags are ignored entirely, and the
  // composerTarget → preflight dependency fails closed.
  await page.goto('/tests/e2e/fixtures/module-harness.html?capabilityUx=composerTarget,report', { waitUntil: 'domcontentloaded' });
  const withList = await page.evaluate(async () => {
    const mod = await import('/components/settings/providers/capabilityUxFlags.ts' as string);
    return mod.getCapabilityUxFlags({
      featureFlags: { capabilityUx: { preflight: true, controls: true } },
    });
  });
  expect(withList).toEqual({
    report: true, editorV2: false, controls: false, preflight: false, composerTarget: false,
  });

  await page.goto('/tests/e2e/fixtures/module-harness.html?capabilityUx=composerTarget,preflight', { waitUntil: 'domcontentloaded' });
  const withDependency = await page.evaluate(async () => {
    const mod = await import('/components/settings/providers/capabilityUxFlags.ts' as string);
    return mod.getCapabilityUxFlags(null);
  });
  expect(withDependency.preflight).toBe(true);
  expect(withDependency.composerTarget).toBe(true);

  // No param: persisted flags apply, dependency still enforced.
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });
  const persistedOnly = await page.evaluate(async () => {
    const mod = await import('/components/settings/providers/capabilityUxFlags.ts' as string);
    return {
      withoutPreflight: mod.getCapabilityUxFlags({ featureFlags: { capabilityUx: { composerTarget: true } } }),
      withPreflight: mod.getCapabilityUxFlags({ featureFlags: { capabilityUx: { composerTarget: true, preflight: true } } }),
    };
  });
  expect(persistedOnly.withoutPreflight.composerTarget).toBe(false);
  expect(persistedOnly.withPreflight.composerTarget).toBe(true);
});

test('auth subject resolution fails closed for an explicit target with a missing connection', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });
  const result = await page.evaluate(async () => {
    const mod = await import('/lib/capability/providerConfig.ts' as string);
    const active = { id: 'cfg-active', displayName: 'Active', providerId: 'anthropic', model: 'claude-opus-4-6' };
    const other = { id: 'cfg-other', displayName: 'Other', providerId: 'bedrock', model: 'model-b' };
    const configs = [active, other];
    return {
      explicitFound: mod.resolveAuthTargetConfig({ connectionId: 'cfg-other', model: 'model-b' }, configs, active)?.id,
      explicitMissing: mod.resolveAuthTargetConfig({ connectionId: 'cfg-deleted', model: 'x' }, configs, active),
      nullTargetFallsBack: mod.resolveAuthTargetConfig(null, configs, active)?.id,
      undefinedTargetFallsBack: mod.resolveAuthTargetConfig(undefined, configs, active)?.id,
    };
  });

  expect(result.explicitFound).toBe('cfg-other');
  // An explicit target NEVER silently falls back to the global default.
  expect(result.explicitMissing).toBeNull();
  expect(result.nullTargetFallsBack).toBe('cfg-active');
  expect(result.undefinedTargetFallsBack).toBe('cfg-active');
});

test('compatible auth resolution never borrows an unrelated legacy profile for a saved connection', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });
  const result = await page.evaluate(async () => {
    const mod = await import('/lib/capability/providerConfig.ts' as string);
    const saved = {
      id: 'cfg-compatible-new', displayName: 'New endpoint', providerId: 'compatible',
      model: 'model-new',
    };
    const profiles = [{
      id: 'legacy-other', displayName: 'Legacy endpoint', apiType: 'chat_completions',
      baseUrl: 'https://legacy.invalid/v1', auth: { type: 'api_key' }, apiKey: 'wrong-key',
      extraHeaders: [],
    }];
    return {
      savedMissing: mod.resolveCompatibleAuthProfile(saved, saved.id, profiles),
      savedExact: mod.resolveCompatibleAuthProfile(saved, saved.id, [
        ...profiles,
        { ...profiles[0], id: saved.id, displayName: 'Exact endpoint' },
      ])?.id,
      legacyFallback: mod.resolveCompatibleAuthProfile(null, 'missing', profiles)?.id,
    };
  });

  expect(result.savedMissing).toBeNull();
  expect(result.savedExact).toBe('cfg-compatible-new');
  expect(result.legacyFallback).toBe('legacy-other');
});
