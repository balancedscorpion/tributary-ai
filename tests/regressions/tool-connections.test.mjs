import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

let connectionConfigPromise;
async function loadConnectionConfig() {
  if (!connectionConfigPromise) {
    connectionConfigPromise = (async () => {
      const { build } = await import('esbuild');
      const result = await build({
        absWorkingDir: process.cwd(),
        entryPoints: ['lib/connectionConfig.ts'],
        bundle: true,
        format: 'esm',
        platform: 'node',
        write: false,
      });
      const code = result.outputFiles[0].text;
      const file = path.join(os.tmpdir(), `tool-connections-${process.pid}-${Date.now()}.mjs`);
      fs.writeFileSync(file, code);
      try {
        return await import(pathToFileURL(file).href);
      } finally {
        fs.unlinkSync(file);
      }
    })();
  }
  return connectionConfigPromise;
}

let permissionRuleSubjectsPromise;
async function loadPermissionRuleSubjects() {
  if (!permissionRuleSubjectsPromise) {
    permissionRuleSubjectsPromise = (async () => {
      const { transformSync } = await import('esbuild');
      const { code } = transformSync(sourceAt('lib/permissionRuleSubjects.ts'), { loader: 'ts', format: 'esm' });
      const file = path.join(os.tmpdir(), `permission-rule-subjects-${process.pid}-${Date.now()}.mjs`);
      fs.writeFileSync(file, code);
      try {
        return await import(pathToFileURL(file).href);
      } finally {
        fs.unlinkSync(file);
      }
    })();
  }
  return permissionRuleSubjectsPromise;
}

const provider = (overrides = {}) => ({
  id: 'provider-1',
  displayName: 'Primary provider',
  providerId: 'openai',
  model: 'gpt-5',
  apiType: 'responses',
  capabilityControls: {
    hosted_web_search: 'off',
    hosted_code_execution: 'off',
    compaction: 'auto',
  },
  webSearch: { enabled: false, searchContextSize: 'low' },
  codeExecution: { enabled: false, containerMode: 'auto', memoryLimit: '2g' },
  ...overrides,
});

test('hosted tool connection merges update only their provider capability slice', async () => {
  const { mergeConnectionPatch } = await loadConnectionConfig();
  const saved = {
    systemPrompt: 'saved',
    providerConfigs: [provider()],
  };
  const local = {
    systemPrompt: 'unrelated draft',
    providerConfigs: [provider({
      model: 'unrelated-model-draft',
      capabilityControls: {
        hosted_web_search: 'on',
        hosted_code_execution: 'off',
        compaction: 'off',
      },
      webSearch: { enabled: true, searchContextSize: 'high', allowedDomains: ['openai.com'] },
      codeExecution: { enabled: true, containerMode: 'existing', containerId: 'draft-container' },
    })],
  };

  const web = mergeConnectionPatch(saved, 'web-search', local);
  assert.equal(web.systemPrompt, 'saved');
  assert.equal(web.providerConfigs[0].model, 'gpt-5');
  assert.deepEqual(web.providerConfigs[0].webSearch, { enabled: true, toolType: 'web_search' });
  assert.deepEqual(web.providerConfigs[0].codeExecution, saved.providerConfigs[0].codeExecution);
  assert.equal(web.providerConfigs[0].capabilityControls.hosted_web_search, 'on');
  assert.equal(web.providerConfigs[0].capabilityControls.hosted_code_execution, 'off');
  assert.equal(web.providerConfigs[0].capabilityControls.compaction, 'auto');

  const python = mergeConnectionPatch(saved, 'python-sandbox', local);
  assert.equal(python.providerConfigs[0].model, 'gpt-5');
  assert.deepEqual(python.providerConfigs[0].webSearch, saved.providerConfigs[0].webSearch);
  assert.deepEqual(python.providerConfigs[0].codeExecution, local.providerConfigs[0].codeExecution);
  assert.equal(python.providerConfigs[0].capabilityControls.hosted_web_search, 'off');
  assert.equal(python.providerConfigs[0].capabilityControls.hosted_code_execution, 'off');
  assert.equal(python.providerConfigs[0].capabilityControls.compaction, 'auto');
});

test('virtual hosted-tool connections save backend-evaluable permission subjects', async () => {
  const {
    permissionSubjectForToolRule,
    supportsConnectionWidePermissionRule,
  } = await loadPermissionRuleSubjects();
  const webSearch = {
    name: 'openai-responses/web_search',
    category: 'read',
    connectorId: 'openai-responses',
    toolSetId: 'provider-hosted',
    permissionSubjectKind: 'provider_hosted_tool',
    permissionOperation: 'webSearch',
  };
  const python = {
    name: 'anthropic-messages/code_execution',
    category: 'execute',
    connectorId: 'anthropic-messages',
    toolSetId: 'provider-hosted',
    permissionSubjectKind: 'provider_hosted_tool',
    permissionOperation: 'codeExecution',
  };

  assert.equal(supportsConnectionWidePermissionRule(webSearch), false);
  assert.equal(supportsConnectionWidePermissionRule(python), false);
  assert.deepEqual(permissionSubjectForToolRule(webSearch, 'web-search'), {
    kind: 'provider_hosted_tool',
    tool: 'openai-responses/web_search',
    connector: 'openai-responses',
    tool_set_id: 'provider-hosted',
    operation: 'webSearch',
  });
  assert.deepEqual(permissionSubjectForToolRule(python, 'python-sandbox'), {
    kind: 'provider_hosted_tool',
    tool: 'anthropic-messages/code_execution',
    connector: 'anthropic-messages',
    tool_set_id: 'provider-hosted',
    operation: 'codeExecution',
  });

  const github = { name: 'github_status', category: 'read', connectorId: 'github' };
  assert.equal(supportsConnectionWidePermissionRule(github), true);
  assert.equal(permissionSubjectForToolRule(github, 'github').connector, 'github');
});

test('tool capabilities are first-class Connections and no longer provider editor controls', () => {
  const connections = sourceAt('components/settings/McpIntegrationsTab.tsx');
  const providers = sourceAt('components/settings/ProvidersTab.tsx');
  const catalog = sourceAt('lib/connectionsCatalog.ts');
  const inventory = sourceAt('lib/toolInventory.ts');
  const app = sourceAt('App.tsx');

  for (const id of ['image-generation', 'web-search', 'python-sandbox']) {
    assert.ok(connections.includes(`id: '${id}'`), `${id} must be offered by Add connection`);
    assert.ok(connections.includes(`if (id === '${id}')`), `${id} needs a connection view`);
    assert.ok(catalog.includes(`'${id}'`), `${id} needs audit/readiness metadata`);
  }

  assert.ok(!providers.includes('<ImageGenerationSection'), 'Image Generation must not render on Providers');
  assert.ok(providers.includes('isConnectionManagedHostedToolField'), 'provider hosted fields must be hidden without deleting round-trip logic');
  assert.ok(inventory.includes("'image-generation': ["), 'generate_image belongs to the Image Generation connection');
  assert.ok(inventory.includes("tool.permissionOperation === 'webSearch'"), 'hosted web search belongs to its connection');
  assert.ok(inventory.includes("tool.permissionOperation === 'codeExecution'"), 'hosted code execution belongs to its connection');
  assert.ok(app.includes("handleOpenConnections('image-generation')"), 'image failures must deep-link to the connection');
});

test('connection toggles update capability controls, mirrors, and runtime scope', () => {
  const connections = sourceAt('components/settings/McpIntegrationsTab.tsx');
  const scope = sourceAt('server/internal/chat/optionc/tool_scope_filter.go');
  const manager = sourceAt('server/internal/chat/optionc/manager.go');

  assert.ok(connections.includes("[hostedControlKey(kind)]: enabled ? 'on' : 'off'"));
  assert.ok(connections.includes('webSearch: canonicalProviderWebSearch(config.providerId, enabled)'));
  assert.ok(connections.includes('codeExecution: { ...defaultCodeExecution(config), ...(config.codeExecution || {}), enabled }'));
  assert.ok(connections.includes('applyProviderConfigToLegacySettings(settings)'));
  assert.ok(scope.includes('"image-generation", "web-search", "python-sandbox"'));
  assert.ok(manager.includes('enabledImageGeneration && activeRunConnectionScope["image-generation"]'));
  assert.ok(manager.includes('if capability.SettingsKey == "webSearch"'));
  assert.ok(manager.includes('server settings as the source of truth'));
  assert.ok(manager.includes('connectionID = "python-sandbox"'));
});

test('web search connection is a single model-attached toggle without a provider picker', () => {
  const connections = sourceAt('components/settings/McpIntegrationsTab.tsx');
  const start = connections.indexOf("if (id === 'web-search')");
  const end = connections.indexOf("if (id === 'python-sandbox')", start);
  assert.ok(start >= 0 && end > start, 'web search view must be present');
  const view = connections.slice(start, end);

  assert.ok(view.includes("const stage: Stage = on ? 'active' : 'enable';"));
  assert.ok(view.includes("persistHostedToolEnabled('web-search', '', true)"));
  assert.ok(view.includes('hasDetails: false'));
  assert.ok(view.includes('usesSheet: false'));
  assert.ok(!view.includes('sheetBody:'), 'web search must not open a provider setup sheet');
  assert.ok(!view.includes('<CxSelect'), 'web search must not ask for a second provider selection');
});

test('removing hosted tool connections disables stale flags on every provider surface', async () => {
  const { disableHostedToolConnectionAcrossProviders } = await loadConnectionConfig();
  const settings = {
    providerConfigs: [
      provider({ id: 'supported-openai' }),
      provider({
        id: 'unsupported-gemini',
        providerId: 'gemini',
        apiType: 'generate_content',
        capabilityControls: { hosted_web_search: 'on', hosted_code_execution: 'on', compaction: 'auto' },
        webSearch: { enabled: true, searchContextSize: 'high' },
        codeExecution: { enabled: true, memoryLimit: '8g' },
      }),
      provider({
        id: 'unsupported-compatible-surface',
        providerId: 'compatible',
        apiType: 'chat_completions',
        capabilityControls: { hosted_web_search: 'on', hosted_code_execution: 'on' },
        webSearch: { enabled: true },
        codeExecution: { enabled: true },
      }),
    ],
  };

  const withoutWeb = disableHostedToolConnectionAcrossProviders(settings, 'web-search');
  for (const config of withoutWeb.providerConfigs) {
    assert.equal(config.capabilityControls.hosted_web_search, 'off');
  }
  assert.deepEqual(withoutWeb.providerConfigs[0].webSearch, { enabled: false, toolType: 'web_search' });
  assert.equal(withoutWeb.providerConfigs[1].webSearch, null);
  assert.equal(withoutWeb.providerConfigs[2].webSearch, null);
  assert.equal(withoutWeb.providerConfigs[1].capabilityControls.hosted_code_execution, 'on');
  assert.equal(withoutWeb.providerConfigs[1].codeExecution.enabled, true);

  const withoutPython = disableHostedToolConnectionAcrossProviders(settings, 'python-sandbox');
  for (const config of withoutPython.providerConfigs) {
    assert.equal(config.capabilityControls.hosted_code_execution, 'off');
    assert.equal(config.codeExecution?.enabled, false);
  }
  assert.equal(withoutPython.providerConfigs[1].capabilityControls.hosted_web_search, 'on');
  assert.equal(withoutPython.providerConfigs[1].webSearch.enabled, true);
});

test('raw Python Skills JSON participates in close and discard state', () => {
  const connections = sourceAt('components/settings/McpIntegrationsTab.tsx');

  assert.ok(connections.includes('pythonSkillsDraftRef.current = nextDraft'), 'typing skills must update the raw draft ref');
  assert.ok(connections.includes('pythonSkillsDraftRef.current !== pythonSkillsDraftBaselineRef.current'), 'close must compare raw skills with its baseline');
  assert.ok(connections.includes('pythonSkillsDraftBaselineRef.current = restoredDraft'), 'discard must reset the raw draft baseline');
  assert.ok(connections.includes('setPythonSkillsDraft(restoredDraft)'), 'discard must restore the visible JSON draft');
});
