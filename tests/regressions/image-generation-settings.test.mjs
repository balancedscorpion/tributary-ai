import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// migrateSettings pulls in constants + capability + compaction modules, so we
// bundle the real graph (matching the compile-and-import spirit of
// artifact-download-naming.test.mjs, extended to a multi-module entry) and
// exercise the actual normalizer rather than asserting on source text.
let migrationPromise = null;
async function loadMigration() {
  if (!migrationPromise) {
    migrationPromise = (async () => {
      const esbuild = await import('esbuild');
      const root = process.cwd();
      const dir = fs.mkdtempSync(path.join(os.tmpdir(), `image-gen-settings-${process.pid}-`));
      const outFile = path.join(dir, 'settingsMigration.mjs');
      await esbuild.build({
        entryPoints: [path.join(root, 'services/settingsMigration.ts')],
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
  return migrationPromise;
}

// A minimal already-migrated settings object the migrator accepts on its
// "already new" branch (isRecord(provider) + Array.isArray(compatibleProfiles)),
// so we isolate imageGeneration normalization and prove raw values survive.
function baseSettings(overrides = {}) {
  return {
    provider: { providerId: 'openai', model: 'gpt-4o' },
    compatibleProfiles: [],
    systemPrompt: '',
    providerConfigs: [],
    ...overrides,
  };
}

test('absent imageGeneration block gets defaults (disabled, gpt-image-2, one image)', async () => {
  const { migrateSettings } = await loadMigration();
  const out = migrateSettings(baseSettings());
  assert.ok(out.imageGeneration, 'imageGeneration block should be present');
  assert.equal(out.imageGeneration.enabled, false);
  assert.equal(out.imageGeneration.model, 'gpt-image-2');
  assert.equal(out.imageGeneration.maxImagesPerRun, 1);
  assert.deepEqual(out.imageGeneration.defaults, {
    quality: 'auto',
    size: 'auto',
    format: 'png',
  });
  assert.equal(out.imageGeneration.providerId, null);
});

test('legacy settings object gains an imageGeneration block', async () => {
  const { migrateSettings } = await loadMigration();
  // No version marker + legacy-only keys drive a non-"already new" branch.
  const out = migrateSettings({
    systemPrompt: 'hi',
    provider: 'openai',
    apiKeys: {},
  });
  assert.ok(out.imageGeneration, 'legacy migration should add the block');
  assert.equal(out.imageGeneration.enabled, false);
  assert.equal(out.imageGeneration.model, 'gpt-image-2');
  assert.equal(out.imageGeneration.maxImagesPerRun, 1);
});

test('enabled stays false by default even for fully-migrated input', async () => {
  const { migrateSettings } = await loadMigration();
  const out = migrateSettings(baseSettings({ imageGeneration: {} }));
  assert.equal(out.imageGeneration.enabled, false);
});

test('invalid values are clamped and coerced', async () => {
  const { migrateSettings } = await loadMigration();
  const out = migrateSettings(baseSettings({
    imageGeneration: {
      enabled: 'yes',            // non-boolean -> default false
      providerId: '   ',         // blank -> null
      model: '   ',              // blank -> default model
      maxImagesPerRun: 99,       // above range -> 4
      defaults: { quality: '', size: 42, format: null },
    },
  }));
  assert.equal(out.imageGeneration.enabled, false);
  assert.equal(out.imageGeneration.providerId, null);
  assert.equal(out.imageGeneration.model, 'gpt-image-2');
  assert.equal(out.imageGeneration.maxImagesPerRun, 4);
  assert.equal(out.imageGeneration.defaults.quality, 'auto');
  assert.equal(out.imageGeneration.defaults.size, 'auto');
  assert.equal(out.imageGeneration.defaults.format, 'png');
});

test('maxImagesPerRun below range clamps up and fractionals round', async () => {
  const { migrateSettings } = await loadMigration();
  const low = migrateSettings(baseSettings({ imageGeneration: { maxImagesPerRun: 0 } }));
  assert.equal(low.imageGeneration.maxImagesPerRun, 1);
  const frac = migrateSettings(baseSettings({ imageGeneration: { maxImagesPerRun: 2.7 } }));
  assert.equal(frac.imageGeneration.maxImagesPerRun, 3);
});

test('existing valid values (including unknown-but-valid strings) are preserved', async () => {
  const { migrateSettings } = await loadMigration();
  const out = migrateSettings(baseSettings({
    providerConfigs: [{ id: 'prov-1', displayName: 'My OpenAI', providerId: 'openai', model: 'gpt-4o' }],
    imageGeneration: {
      enabled: true,
      providerId: 'prov-1',
      model: 'gpt-image-3-preview',       // future model kept
      maxImagesPerRun: 3,
      defaults: { quality: 'ultra', size: '1536x1024', format: 'webp' },
    },
  }));
  assert.equal(out.imageGeneration.enabled, true);
  assert.equal(out.imageGeneration.providerId, 'prov-1');
  assert.equal(out.imageGeneration.model, 'gpt-image-3-preview');
  assert.equal(out.imageGeneration.maxImagesPerRun, 3);
  assert.equal(out.imageGeneration.defaults.quality, 'ultra');
  assert.equal(out.imageGeneration.defaults.size, '1536x1024');
  assert.equal(out.imageGeneration.defaults.format, 'webp');
});

test('enabled tool capabilities are discoverable as Connections after migration', async () => {
  const { migrateSettings } = await loadMigration();
  const out = migrateSettings(baseSettings({
    connections: { visibleIds: [] },
    providerConfigs: [{
      id: 'prov-1',
      displayName: 'My OpenAI',
      providerId: 'openai',
      model: 'gpt-5',
      apiType: 'responses',
      capabilityControls: {
        hosted_web_search: 'on',
        hosted_code_execution: 'on',
      },
      webSearch: { enabled: true },
      codeExecution: { enabled: true, containerMode: 'auto' },
    }],
    imageGeneration: {
      enabled: true,
      providerId: 'prov-1',
      model: 'gpt-image-2',
    },
  }));

  assert.ok(out.connections.visibleIds.includes('image-generation'));
  assert.ok(out.connections.visibleIds.includes('web-search'));
  assert.ok(out.connections.visibleIds.includes('python-sandbox'));
});

test('unsupported provider surfaces cannot resurrect hosted tool Connections during migration', async () => {
  const { migrateSettings } = await loadMigration();
  const out = migrateSettings(baseSettings({
    connections: { visibleIds: [] },
    providerConfigs: [{
      id: 'legacy-chat-completions',
      displayName: 'Legacy endpoint',
      providerId: 'compatible',
      model: 'legacy-model',
      apiType: 'chat_completions',
      apiSurface: 'chat_completions',
      capabilityControls: {
        hosted_web_search: 'on',
        hosted_code_execution: 'on',
      },
      webSearch: { enabled: true },
      codeExecution: { enabled: true, containerMode: 'auto' },
    }],
  }));

  assert.ok(!out.connections.visibleIds.includes('web-search'));
  assert.ok(!out.connections.visibleIds.includes('python-sandbox'));
});
