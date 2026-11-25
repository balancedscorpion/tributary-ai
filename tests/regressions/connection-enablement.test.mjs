import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// The Connections settings tab and the chat drawer pill are two editors of ONE
// enabled-for-run connection state: the per-connection enabled flags in
// AppSettings. These tests exercise the shared mutation both surfaces use
// (lib/connectionEnablement.ts) against the real readiness derivation
// (lib/connectionsCatalog.ts), plus source-level wire-contract honesty: the
// frontend no longer sends a client-derived connection scope with runs, while
// the backend keeps accepting one from older clients.

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

let enablementPromise = null;
async function loadEnablement() {
  if (!enablementPromise) {
    enablementPromise = (async () => {
      const esbuild = await import('esbuild');
      const root = process.cwd();
      const dir = fs.mkdtempSync(path.join(os.tmpdir(), `connection-enablement-${process.pid}-`));
      const outFile = path.join(dir, 'connectionEnablement.mjs');
      await esbuild.build({
        stdin: {
          contents: [
            "export * from '@/lib/connectionEnablement';",
            "export { getReadyConnectionIds, deriveVisibleConnections } from '@/lib/connectionsCatalog';",
            "export { migrateSettings } from '@/services/settingsMigration';",
          ].join('\n'),
          resolveDir: root,
          loader: 'ts',
        },
        bundle: true,
        format: 'esm',
        platform: 'node',
        outfile: outFile,
        absWorkingDir: root,
        logLevel: 'silent',
        alias: { '@': root },
      });
      try {
        return await import(pathToFileURL(outFile).href);
      } finally {
        fs.rmSync(dir, { recursive: true, force: true });
      }
    })();
  }
  return enablementPromise;
}

const hostedProvider = (overrides = {}) => ({
  id: 'provider-1',
  displayName: 'Primary provider',
  providerId: 'openai',
  model: 'gpt-5',
  apiType: 'responses',
  capabilityControls: { hosted_web_search: 'off', hosted_code_execution: 'off' },
  webSearch: { enabled: false, searchContextSize: 'low' },
  codeExecution: { enabled: false, containerMode: 'auto' },
  ...overrides,
});

function settingsFixture(overrides = {}) {
  return {
    provider: { providerId: 'openai', model: 'gpt-5' },
    compatibleProfiles: [],
    providerConfigs: [],
    connections: {
      visibleIds: [
        'thread-search', 'image-generation', 'web-search', 'python-sandbox',
        'local-computer', 'bigquery', 'databricks', 'trino', 'snowflake',
        'github', 'slack', 'atlassian', 'sharepoint', 'browser', 'arxiv',
      ],
    },
    threadSearch: { enabled: true },
    localComputer: { enabled: true, accessMode: 'whole-computer', selectedFolders: [] },
    dataSources: {
      bigquery: { enabled: true },
      databricks: { enabled: true },
      trino: { enabled: true },
      snowflake: { enabled: true },
    },
    github: { enabled: true, readTools: true, allowedRepositories: ['acme/data'] },
    slack: { enabled: true, readTools: true, writeTools: false },
    atlassian: {
      jiraBaseUrl: 'https://acme.atlassian.net',
      jira: { enabled: true },
      confluence: { enabled: false },
    },
    sharepoint: { enabled: true, siteUrl: 'https://acme.sharepoint.com/sites/data' },
    browserControl: { enabled: true },
    arxiv: { enabled: true },
    imageGeneration: { enabled: true, providerId: 'provider-1', model: 'gpt-image-2' },
    ...overrides,
  };
}

test('disable then re-enable round-trips through the canonical state for every simple connection', async () => {
  const { withConnectionEnabled, getReadyConnectionIds } = await loadEnablement();
  const base = settingsFixture({ providerConfigs: [hostedProvider()] });
  const simple = [
    'thread-search', 'local-computer', 'github', 'slack', 'sharepoint',
    'browser', 'arxiv', 'bigquery', 'databricks', 'trino', 'snowflake',
    'image-generation',
  ];
  for (const id of simple) {
    assert.ok(getReadyConnectionIds(base).includes(id), `${id} should start ready`);
    const off = withConnectionEnabled(base, id, false);
    assert.ok(!getReadyConnectionIds(off).includes(id), `${id} should be off after disable`);
    const backOn = withConnectionEnabled(off, id, true);
    assert.ok(getReadyConnectionIds(backOn).includes(id), `${id} should be ready again after re-enable`);
  }
});

test('enable/disable never touches configuration or credentials', async () => {
  const { withConnectionEnabled } = await loadEnablement();
  const base = settingsFixture();
  const off = withConnectionEnabled(base, 'github', false);
  assert.equal(off.github.enabled, false);
  assert.deepEqual(off.github.allowedRepositories, ['acme/data']);
  assert.equal(off.github.readTools, true);
  const on = withConnectionEnabled(off, 'github', true);
  assert.equal(on.github.enabled, true);
  assert.deepEqual(on.github.allowedRepositories, ['acme/data']);
});

test('re-enabling legacy GitHub and Slack off states restores effective tool access', async () => {
  const { withConnectionEnabled } = await loadEnablement();
  const legacyOff = settingsFixture({
    github: { enabled: false, readTools: false, allowedRepositories: ['acme/data'] },
    slack: { enabled: false, readTools: false, writeTools: false, allowedChannelIds: ['C123'] },
  });

  const githubOn = withConnectionEnabled(legacyOff, 'github', true);
  assert.equal(githubOn.github.enabled, true);
  assert.equal(githubOn.github.readTools, true);
  assert.deepEqual(githubOn.github.allowedRepositories, ['acme/data']);

  const slackOn = withConnectionEnabled(legacyOff, 'slack', true);
  assert.equal(slackOn.slack.enabled, true);
  assert.equal(slackOn.slack.readTools, true);
  assert.equal(slackOn.slack.writeTools, false);
  assert.deepEqual(slackOn.slack.allowedChannelIds, ['C123']);
});

test('atlassian re-enable restores only products with a configured base URL', async () => {
  const { withConnectionEnabled, getReadyConnectionIds } = await loadEnablement();
  const base = settingsFixture();
  const off = withConnectionEnabled(base, 'atlassian', false);
  assert.equal(off.atlassian.jira.enabled, false);
  assert.equal(off.atlassian.confluence.enabled, false);
  assert.ok(!getReadyConnectionIds(off).includes('atlassian'));
  const on = withConnectionEnabled(off, 'atlassian', true);
  assert.equal(on.atlassian.jira.enabled, true, 'jira has a base URL, so it re-enables');
  assert.equal(on.atlassian.confluence.enabled, false, 'confluence has no base URL, so it stays off');
  assert.ok(getReadyConnectionIds(on).includes('atlassian'));
});

test('unconfigured connections refuse a plain enable and report canEnableConnection=false', async () => {
  const { withConnectionEnabled, canEnableConnection } = await loadEnablement();
  const bare = settingsFixture({
    atlassian: { jira: { enabled: false }, confluence: { enabled: false } },
    imageGeneration: { enabled: false, providerId: 'missing-provider' },
    providerConfigs: [],
  });
  // No Atlassian base URLs: flipping flags would not produce working tools.
  assert.equal(withConnectionEnabled(bare, 'atlassian', true), bare, 'atlassian enable must be refused (identity return)');
  assert.equal(canEnableConnection(bare, 'atlassian'), false);
  // Image generation with a dangling provider reference.
  assert.equal(withConnectionEnabled(bare, 'image-generation', true), bare);
  assert.equal(canEnableConnection(bare, 'image-generation'), false);
  // Hosted tools with no provider connections at all.
  assert.equal(withConnectionEnabled(bare, 'web-search', true), bare);
  assert.equal(canEnableConnection(bare, 'web-search'), false);
  // Credential/setup-backed connections must not be presented as a simple
  // switch merely because setting enabled=true would make the old catalogue
  // call them ready.
  assert.equal(canEnableConnection({ ...bare, github: { enabled: false, readTools: false } }, 'github'), false);
  assert.equal(canEnableConnection({ ...bare, slack: { enabled: false, readTools: false } }, 'slack'), false);
  assert.equal(canEnableConnection({
    ...bare,
    googleProjectId: '',
    dataSources: { ...bare.dataSources, bigquery: { enabled: false } },
  }, 'bigquery'), false);
  assert.equal(canEnableConnection({
    ...bare,
    localComputer: { enabled: false, accessMode: 'whole-computer' },
  }, 'local-computer'), false);
});

test('web-search is one toggle attached to every supported selected-model target', async () => {
  const { withConnectionEnabled, getReadyConnectionIds, canEnableConnection } = await loadEnablement();
  const base = settingsFixture({
    activeProviderConfigId: 'openai-fresh',
    providerConfigs: [
      hostedProvider({
        id: 'openai-fresh',
        displayName: 'OpenAI fresh',
        webSearch: null,
      }),
      hostedProvider({
        id: 'anthropic-fresh',
        displayName: 'Anthropic fresh',
        providerId: 'anthropic',
        apiType: 'messages',
        model: 'claude-sonnet-4-6',
        webSearch: null,
      }),
      hostedProvider({
        id: 'gemini-unsupported',
        displayName: 'Gemini',
        providerId: 'gemini',
        apiType: 'generate_content',
        model: 'gemini-3',
        webSearch: null,
      }),
    ],
  });
  assert.ok(!getReadyConnectionIds(base).includes('web-search'), 'starts off (control off)');
  assert.equal(canEnableConnection(base, 'web-search'), true, 'a supported selected-model target is enough');
  const on = withConnectionEnabled(base, 'web-search', true);
  const openai = on.providerConfigs.find((config) => config.id === 'openai-fresh');
  const anthropic = on.providerConfigs.find((config) => config.id === 'anthropic-fresh');
  const gemini = on.providerConfigs.find((config) => config.id === 'gemini-unsupported');
  assert.equal(openai.capabilityControls.hosted_web_search, 'on');
  assert.deepEqual(openai.webSearch, { enabled: true, toolType: 'web_search' });
  assert.equal(anthropic.capabilityControls.hosted_web_search, 'on');
  assert.equal(anthropic.webSearch.enabled, true);
  assert.deepEqual(anthropic.webSearch.allowedCallers, ['direct']);
  assert.equal(gemini.capabilityControls.hosted_web_search, 'off', 'unsupported providers stay untouched');
  assert.equal(gemini.webSearch, null);
  assert.ok(getReadyConnectionIds(on).includes('web-search'));
  const off = withConnectionEnabled(on, 'web-search', false);
  for (const id of ['openai-fresh', 'anthropic-fresh']) {
    const offTarget = off.providerConfigs.find((config) => config.id === id);
    assert.equal(offTarget.capabilityControls.hosted_web_search, 'off');
    assert.equal(offTarget.webSearch.enabled, false);
  }
  assert.deepEqual(
    off.providerConfigs.find((config) => config.id === 'openai-fresh').webSearch,
    { enabled: false, toolType: 'web_search' },
  );
  assert.ok(!getReadyConnectionIds(off).includes('web-search'));
});

test('new model connections inherit the enabled global web-search connection', async () => {
  const { withConnectionEnabled, isConnectionEnabledForRuns } = await loadEnablement();
  const before = withConnectionEnabled(settingsFixture({
    activeProviderConfigId: 'openai-existing',
    providerConfigs: [hostedProvider({ id: 'openai-existing' })],
  }), 'web-search', true);
  assert.equal(isConnectionEnabledForRuns(before, 'web-search'), true);

  const afterSave = {
    ...before,
    activeProviderConfigId: 'anthropic-new',
    providerConfigs: [
      ...before.providerConfigs,
      hostedProvider({
        id: 'anthropic-new',
        providerId: 'anthropic',
        apiType: 'messages',
        model: 'claude-sonnet-4-6',
        webSearch: null,
      }),
    ],
  };
  const inherited = withConnectionEnabled(afterSave, 'web-search', true);
  const anthropic = inherited.providerConfigs.find((config) => config.id === 'anthropic-new');
  assert.equal(anthropic.capabilityControls.hosted_web_search, 'on');
  assert.equal(anthropic.webSearch.enabled, true);
  assert.equal(anthropic.webSearch.toolType, 'web_search_20260209');
  assert.deepEqual(anthropic.webSearch.allowedCallers, ['direct']);

  const providersTab = sourceAt('components/settings/ProvidersTab.tsx');
  assert.ok(
    providersTab.includes("isConnectionEnabledForRuns(localSettings, 'web-search')"),
    'the provider wizard checks the existing global connection state before saving',
  );
  assert.ok(
    providersTab.includes("withConnectionEnabled(nextSettings, 'web-search', true)"),
    'the provider wizard applies the shared provider-native web-search mutation to the new config',
  );
});

test('settings migration permanently repairs split Web Search state from older app versions', async () => {
  const { migrateSettings, getReadyConnectionIds, deriveVisibleConnections } = await loadEnablement();
  const migrated = migrateSettings(settingsFixture({
    provider: { providerId: 'compatible', model: 'gpt-5.5', profileId: 'azure-responses' },
    activeProviderConfigId: 'azure-responses',
    providerConfigs: [
      hostedProvider({
        id: 'old-openai',
        displayName: 'Old OpenAI',
        capabilityControls: { hosted_web_search: 'on', hosted_code_execution: 'off' },
        webSearch: { enabled: true },
      }),
      hostedProvider({
        id: 'azure-responses',
        displayName: 'Azure GPT 5.5',
        providerId: 'compatible',
        apiType: 'responses',
        apiSurface: 'responses',
        baseUrl: 'https://example.openai.azure.com/openai/v1',
        capabilityControls: { hosted_web_search: 'off', hosted_code_execution: 'off' },
        webSearch: { enabled: false },
      }),
      hostedProvider({
        id: 'anthropic-auto',
        displayName: 'Anthropic Auto',
        providerId: 'anthropic',
        apiType: 'messages',
        apiSurface: 'messages',
        capabilityControls: { hosted_web_search: 'auto', hosted_code_execution: 'off' },
        webSearch: { enabled: true },
      }),
      hostedProvider({
        id: 'gemini-unsupported',
        displayName: 'Gemini',
        providerId: 'gemini',
        apiType: 'generate_content',
        apiSurface: 'generate_content',
        webSearch: null,
      }),
    ],
  }));

  for (const id of ['old-openai', 'azure-responses', 'anthropic-auto']) {
    const config = migrated.providerConfigs.find((candidate) => candidate.id === id);
    assert.equal(config.capabilityControls.hosted_web_search, 'on', `${id} control is repaired`);
    assert.equal(config.webSearch.enabled, true, `${id} mirror is repaired`);
  }
  const anthropic = migrated.providerConfigs.find((config) => config.id === 'anthropic-auto');
  assert.equal(anthropic.webSearch.toolType, 'web_search_20260209');
  assert.deepEqual(anthropic.webSearch.allowedCallers, ['direct']);
  const gemini = migrated.providerConfigs.find((config) => config.id === 'gemini-unsupported');
  assert.equal(gemini.capabilityControls.hosted_web_search, 'off', 'unsupported provider stays untouched');
  assert.equal(gemini.webSearch, null);
  assert.ok(getReadyConnectionIds(migrated).includes('web-search'));
  const web = deriveVisibleConnections(migrated).find((connection) => connection.id === 'web-search');
  assert.match(web.identity, /Old OpenAI/);
  assert.match(web.identity, /Azure GPT 5\.5/);
});

test('settings migration removes stale Web Search tool schemas from every legacy mirror', async () => {
  const { migrateSettings } = await loadEnablement();
  const staleOpenAI = {
    enabled: true,
    toolType: 'web_search_preview',
    maxUses: 99,
    searchContextSize: 'legacy',
    allowedDomains: ['old.example'],
    blockedDomains: ['new.example'],
    allowedCallers: ['code_execution_20250522'],
  };
  const migrated = migrateSettings(settingsFixture({
    provider: { providerId: 'compatible', model: 'gpt-5.5', profileId: 'azure-responses' },
    activeProviderConfigId: 'azure-responses',
    openai: { apiKey: '', baseUrl: 'https://api.openai.com/v1', webSearch: staleOpenAI },
    anthropic: {
      apiKey: '',
      baseUrl: 'https://api.anthropic.com',
      webSearch: { ...staleOpenAI, toolType: 'web_search_20250305' },
    },
    compatibleProfiles: [
      {
        id: 'azure-responses',
        displayName: 'Azure Responses',
        apiType: 'responses',
        baseUrl: 'https://example.openai.azure.com/openai/v1',
        auth: { type: 'api_key', header: 'api-key', scheme: null },
        extraHeaders: [],
        webSearch: staleOpenAI,
      },
      {
        id: 'legacy-chat',
        displayName: 'Legacy chat',
        apiType: 'chat_completions',
        baseUrl: 'https://api.openai.com/v1',
        auth: { type: 'api_key', header: 'Authorization', scheme: 'Bearer' },
        extraHeaders: [],
        webSearch: staleOpenAI,
      },
    ],
    providerConfigs: [
      hostedProvider({
        id: 'openai-old-schema',
        capabilityControls: { hosted_web_search: 'on', hosted_code_execution: 'off' },
        webSearch: staleOpenAI,
      }),
      hostedProvider({
        id: 'azure-responses',
        providerId: 'compatible',
        apiType: 'responses',
        apiSurface: 'responses',
        capabilityControls: { hosted_web_search: 'off', hosted_code_execution: 'off' },
        webSearch: { ...staleOpenAI, enabled: false },
      }),
      hostedProvider({
        id: 'anthropic-old-schema',
        providerId: 'anthropic',
        apiType: 'messages',
        apiSurface: 'messages',
        capabilityControls: { hosted_web_search: 'off', hosted_code_execution: 'off' },
        webSearch: { ...staleOpenAI, enabled: false, toolType: 'web_search_20250305' },
      }),
      hostedProvider({
        id: 'compatible-chat-old-schema',
        providerId: 'compatible',
        apiType: 'chat_completions',
        apiSurface: 'chat_completions',
        capabilityControls: { hosted_web_search: 'on', hosted_code_execution: 'off' },
        webSearch: staleOpenAI,
      }),
    ],
  }));

  assert.deepEqual(
    migrated.providerConfigs.find((config) => config.id === 'openai-old-schema').webSearch,
    { enabled: true, toolType: 'web_search' },
  );
  assert.deepEqual(
    migrated.providerConfigs.find((config) => config.id === 'azure-responses').webSearch,
    { enabled: true, toolType: 'web_search' },
  );
  assert.deepEqual(
    migrated.providerConfigs.find((config) => config.id === 'anthropic-old-schema').webSearch,
    { enabled: true, toolType: 'web_search_20260209', maxUses: 5, allowedCallers: ['direct'] },
  );
  const incompatible = migrated.providerConfigs.find((config) => config.id === 'compatible-chat-old-schema');
  assert.equal(incompatible.capabilityControls.hosted_web_search, 'off');
  assert.equal(incompatible.webSearch, null);
  assert.deepEqual(migrated.openai.webSearch, { enabled: true, toolType: 'web_search' });
  assert.deepEqual(
    migrated.anthropic.webSearch,
    { enabled: true, toolType: 'web_search_20260209', maxUses: 5, allowedCallers: ['direct'] },
  );
  assert.deepEqual(
    migrated.compatibleProfiles.find((profile) => profile.id === 'azure-responses').webSearch,
    { enabled: true, toolType: 'web_search' },
  );
  assert.equal(
    migrated.compatibleProfiles.find((profile) => profile.id === 'legacy-chat').webSearch,
    null,
  );
});

test('explicit Auto does not resurrect Web Search from a stale enabled mirror', async () => {
  const { migrateSettings, getReadyConnectionIds } = await loadEnablement();
  const migrated = migrateSettings(settingsFixture({
    providerConfigs: [
      hostedProvider({
        id: 'openai-auto',
        capabilityControls: { hosted_web_search: 'auto', hosted_code_execution: 'off' },
        webSearch: { enabled: true },
      }),
      hostedProvider({
        id: 'azure-off',
        providerId: 'compatible',
        apiType: 'responses',
        apiSurface: 'responses',
        capabilityControls: { hosted_web_search: 'off', hosted_code_execution: 'off' },
        webSearch: { enabled: false },
      }),
    ],
  }));

  for (const config of migrated.providerConfigs) {
    assert.equal(config.capabilityControls.hosted_web_search, 'off');
    assert.equal(config.webSearch.enabled, false);
  }
  assert.ok(!getReadyConnectionIds(migrated).includes('web-search'));
});

test('unsupported-only legacy settings cannot keep an obsolete Web Search block', async () => {
  const { migrateSettings } = await loadEnablement();
  const migrated = migrateSettings(settingsFixture({
    provider: { providerId: 'compatible', model: 'legacy-chat', profileId: 'legacy-chat' },
    activeProviderConfigId: 'legacy-chat',
    compatibleProfiles: [{
      id: 'legacy-chat',
      displayName: 'Legacy chat',
      apiType: 'chat_completions',
      baseUrl: 'https://api.openai.com/v1',
      auth: { type: 'api_key', header: 'Authorization', scheme: 'Bearer' },
      extraHeaders: [],
      webSearch: { enabled: true, toolType: 'web_search_preview', maxUses: 99 },
    }],
    providerConfigs: [hostedProvider({
      id: 'legacy-chat',
      providerId: 'compatible',
      apiType: 'chat_completions',
      apiSurface: 'chat_completions',
      capabilityControls: { hosted_web_search: 'on', hosted_code_execution: 'off' },
      webSearch: { enabled: true, toolType: 'web_search_preview', maxUses: 99 },
    })],
  }));

  assert.equal(migrated.providerConfigs[0].capabilityControls.hosted_web_search, 'off');
  assert.equal(migrated.providerConfigs[0].webSearch, null);
  assert.equal(migrated.compatibleProfiles[0].webSearch, null);
});

test('Anthropic adapters use direct callers for canonical standalone web search', () => {
  const goProvider = sourceAt('server/internal/chat/providers/anthropic/provider.go');
  const goFactory = sourceAt('server/internal/chat/providers/factory.go');
  const tsAdapter = sourceAt('providers/adapters/anthropicHttp.ts');
  assert.ok(goProvider.includes('tool["allowed_callers"]'));
  assert.ok(goFactory.includes('AllowedCallers: []string{"direct"}'));
  assert.ok(tsAdapter.includes('tool.allowed_callers'));
});

test('obsolete Web Search schemas cannot reappear through provider forms or browser caches', () => {
  const providerDefs = sourceAt('providers/catalog/providerDefs.ts');
  for (const key of [
    'webSearchToolType', 'webSearchMaxUses', 'webSearchSearchContextSize',
    'webSearchAllowedDomains', 'webSearchBlockedDomains', 'webSearchAllowedCallers',
  ]) {
    assert.ok(!providerDefs.includes(key), `provider editor must not expose obsolete ${key}`);
  }

  const providersTab = sourceAt('components/settings/ProvidersTab.tsx');
  assert.ok(!providersTab.includes('webSearchToolType:'), 'provider drafts cannot hydrate a persisted tool type');
  assert.ok(
    providersTab.includes('webSearch: canonicalProviderWebSearch(selProv, fields.webSearchEnabled === true)'),
    'provider capability previews use the canonical provider-native schema'
  );

  const settingsHook = sourceAt('hooks/useSettings.ts');
  assert.ok(settingsHook.includes("cache: 'no-store'"), 'settings fetches bypass any cached API response');
  const desktop = sourceAt('src-tauri/src/lib.rs');
  assert.ok(desktop.includes('append_pair("desktop_build"'), 'every desktop release identifies its shell URL');
  assert.ok(desktop.includes('append_pair("launch"'), 'every desktop launch uses a fresh shell URL');
});

test('runs no longer carry a client-derived connection scope (canonical state lives server-side)', () => {
  const useChat = sourceAt('hooks/useChat.ts');
  assert.ok(
    !useChat.includes('activeConnectionIds:'),
    'the run POST must not send activeConnectionIds — the backend derives scope from persisted settings'
  );
  assert.ok(
    !useChat.includes('dataSources: settings.dataSources'),
    'the run POST must not send a client dataSources snapshot that can override server-persisted enablement'
  );
  const app = sourceAt('App.tsx');
  assert.ok(!app.includes('activeConnectionIds'), 'App no longer threads a per-thread scope into sends');
  assert.ok(!app.includes('useThreadToolScope'), 'the per-thread localStorage scope hook is retired');
  assert.ok(
    !fs.existsSync(path.join(process.cwd(), 'hooks/useThreadToolScope.ts')),
    'hooks/useThreadToolScope.ts is deleted'
  );
});

test('backend keeps decoding legacy scope fields so older clients stay compatible', () => {
  const handler = sourceAt('server/internal/chat/httpapi/handler.go');
  assert.ok(handler.includes('parseRunActiveConnectionIDs'), 'handler still parses activeConnectionIds');
  assert.ok(handler.includes('parseRunDataSources'), 'handler still parses dataSources');
  const manager = sourceAt('server/internal/chat/optionc/manager.go');
  assert.ok(manager.includes('applyRunActiveConnectionScope'), 'manager still narrows by a provided legacy scope');
});

test('both surfaces edit the same canonical state through the serialized settings mutation', () => {
  const useSettings = sourceAt('hooks/useSettings.ts');
  assert.ok(useSettings.includes('applySettingsMutation'), 'useSettings exposes the serialized mutation');
  assert.ok(useSettings.includes('serverTruthRef'), 'mutations rebase on the latest saved settings, not render-time state');
  const app = sourceAt('App.tsx');
  assert.ok(
    app.includes('applySettingsMutation((latest) => withConnectionEnabled(latest, id, enabled))'),
    'the drawer toggle persists through the shared enablement mutation'
  );
  const pill = sourceAt('components/chat/ToolAuditPill.tsx');
  assert.ok(pill.includes('onSetConnectionEnabled'), 'the pill mutates the shared state');
  assert.ok(!pill.includes('for this chat'), 'per-chat scope wording is gone');
  assert.ok(!pill.includes('resetToDefault'), 'per-chat reset semantics are gone');
  const tab = sourceAt('components/settings/McpIntegrationsTab.tsx');
  assert.ok(tab.includes('onApplySettingsMutation'), 'the Connections tab persists through the same serialized mutation');
});

test('exec-time backend gates receive run-scoped values for github/slack/atlassian/browser', () => {
  const manager = sourceAt('server/internal/chat/optionc/manager.go');
  const execToolSig = manager.slice(manager.indexOf('func (m *Manager) execTool('));
  const sigEnd = execToolSig.indexOf(') (types.ChatMessage, error)');
  const signature = execToolSig.slice(0, sigEnd);
  for (const param of ['enabledAtlassian atlassianEnablement', 'enabledBrowserControl bool', 'enabledGitHub bool', 'enabledSlack slackToolAccess']) {
    assert.ok(signature.includes(param), `execTool signature carries ${param}`);
  }
  const body = execToolSig
    .slice(sigEnd, execToolSig.indexOf('\nfunc ', sigEnd))
    .split('\n')
    .filter((line) => !line.trim().startsWith('//'))
    .join('\n');
  assert.ok(
    !body.includes('m.settings.Get()'),
    'execTool must not re-read live global settings for connection gating'
  );
});
