import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

// ---------------------------------------------------------------------------
// lib/connectionConfig.ts — behavioral tests. The module is pure and only has
// type-level imports, so esbuild can compile it standalone for real execution.
// ---------------------------------------------------------------------------

let connectionConfigPromise = null;
async function loadConnectionConfig() {
  if (!connectionConfigPromise) {
    connectionConfigPromise = (async () => {
      const { transformSync } = await import('esbuild');
      const { code } = transformSync(sourceAt('lib/connectionConfig.ts'), { loader: 'ts', format: 'esm' });
      const file = path.join(os.tmpdir(), `connection-config-${process.pid}-${Date.now()}.mjs`);
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

const baseSettings = () => ({
  systemPrompt: 'base prompt',
  googleProjectId: 'proj-1',
  googleCredentialsPath: '',
  databricksHost: 'https://1.gcp.databricks.com',
  databricksWarehouseId: 'wh-1',
  databricksOAuthClientId: '',
  githubOAuthClientId: '',
  slackOAuthClientId: 'client-1',
  trinoHost: 'https://trino.example.com',
  trinoCatalog: 'hive',
  trinoSchema: 'default',
  trinoUser: 'analyst',
  trinoSource: '',
  snowflakeAccountIdentifier: 'xy12345.us-east-1',
  snowflakeWarehouse: 'WH',
  snowflakeDatabase: 'DB',
  snowflakeSchema: 'PUBLIC',
  snowflakeRole: '',
  snowflakeUser: 'user1',
  threadSearch: { enabled: true },
  browserControl: { enabled: false, browserProfileDir: '' },
  arxiv: { enabled: false },
  github: { enabled: true, readTools: true, allowedRepositories: ['o/r'] },
  slack: { enabled: true, readTools: true, allowedChannelIds: [] },
  atlassian: {
    jiraBaseUrl: 'https://acme.atlassian.net',
    confluenceBaseUrl: 'https://acme.atlassian.net/wiki',
    jira: { enabled: true },
    confluence: { enabled: false },
  },
  sharepoint: { siteUrl: 'https://acme.sharepoint.com', enabled: true, authMode: 'browser' },
  dataSources: {
    bigquery: { enabled: true, lastCheckedAt: 111, lastCheckOk: true, lastProjectId: 'proj-1' },
    databricks: { enabled: true, lastCheckedAt: 222, lastCheckOk: true },
    trino: { enabled: false, lastCheckedAt: 333, lastCheckOk: true },
    snowflake: { enabled: false, lastCheckedAt: 444, lastCheckOk: false },
  },
});

test('getConnectionPatch returns each connection\'s scoped settings slice', async () => {
  const { getConnectionPatch } = await loadConnectionConfig();
  const s = baseSettings();

  const expectKeys = {
    'thread-search': ['threadSearch'],
    browser: ['browserControl'],
    github: ['github', 'githubOAuthClientId'],
    slack: ['slack', 'slackOAuthClientId'],
    atlassian: ['atlassian'],
    sharepoint: ['sharepoint'],
    arxiv: ['arxiv'],
    bigquery: ['googleProjectId', 'googleCredentialsPath', 'dataSources'],
    databricks: ['databricksHost', 'databricksWarehouseId', 'databricksOAuthClientId', 'dataSources'],
    trino: ['trinoHost', 'trinoCatalog', 'trinoSchema', 'trinoUser', 'trinoSource', 'dataSources'],
    snowflake: [
      'snowflakeAccountIdentifier', 'snowflakeWarehouse', 'snowflakeDatabase',
      'snowflakeSchema', 'snowflakeRole', 'snowflakeUser', 'dataSources',
    ],
  };
  for (const [id, keys] of Object.entries(expectKeys)) {
    const patch = getConnectionPatch(s, id);
    assert.deepEqual(Object.keys(patch).sort(), [...keys].sort(), `patch keys for ${id}`);
  }
  // Data-source patches carry only their own dataSources entry.
  assert.deepEqual(Object.keys(getConnectionPatch(s, 'trino').dataSources), ['trino']);
});

test('mergeConnectionPatch persists one slice without carrying unrelated drafts', async () => {
  const { mergeConnectionPatch } = await loadConnectionConfig();
  const saved = baseSettings();
  const local = {
    ...baseSettings(),
    systemPrompt: 'DRAFT prompt edited on another tab',
    googleProjectId: 'DRAFT-project',
    trinoHost: 'https://new-trino.example.com',
    slackOAuthClientId: 'DRAFT-slack-client',
    dataSources: {
      ...baseSettings().dataSources,
      trino: { enabled: true, lastCheckOk: true },
      bigquery: { enabled: false },
    },
  };

  const next = mergeConnectionPatch(saved, 'trino', local);
  // Trino slice comes from local…
  assert.equal(next.trinoHost, 'https://new-trino.example.com');
  assert.deepEqual(next.dataSources.trino, { enabled: true, lastCheckOk: true });
  // …everything else stays as saved.
  assert.equal(next.systemPrompt, 'base prompt');
  assert.equal(next.googleProjectId, 'proj-1');
  assert.equal(next.slackOAuthClientId, 'client-1');
  assert.deepEqual(next.dataSources.bigquery, saved.dataSources.bigquery);
});

test('configFingerprint changes on every setup-relevant field and ignores health', async () => {
  const { configFingerprint } = await loadConnectionConfig();
  const s = baseSettings();

  const mutations = {
    github: [(x) => ({ ...x, githubOAuthClientId: 'changed' })],
    slack: [(x) => ({ ...x, slackOAuthClientId: 'changed' })],
    atlassian: [
      (x) => ({ ...x, atlassian: { ...x.atlassian, jiraBaseUrl: 'https://other.atlassian.net' } }),
      (x) => ({ ...x, atlassian: { ...x.atlassian, confluenceBaseUrl: 'https://other.atlassian.net/wiki' } }),
    ],
    sharepoint: [
      (x) => ({ ...x, sharepoint: { ...x.sharepoint, siteUrl: 'https://other.sharepoint.com' } }),
      (x) => ({ ...x, sharepoint: { ...x.sharepoint, authMode: 'graph' } }),
    ],
    bigquery: [
      (x) => ({ ...x, googleProjectId: 'other-project' }),
      (x) => ({ ...x, googleCredentialsPath: '/tmp/adc.json' }),
    ],
    databricks: [
      (x) => ({ ...x, databricksHost: 'https://2.gcp.databricks.com' }),
      (x) => ({ ...x, databricksWarehouseId: 'wh-2' }),
      (x) => ({ ...x, databricksOAuthClientId: 'custom-client' }),
    ],
    trino: [
      (x) => ({ ...x, trinoHost: 'https://other.example.com' }),
      (x) => ({ ...x, trinoCatalog: 'iceberg' }),
      (x) => ({ ...x, trinoSchema: 'analytics' }),
      (x) => ({ ...x, trinoUser: 'other-user' }),
      (x) => ({ ...x, trinoSource: 'other-source' }),
    ],
    snowflake: [
      (x) => ({ ...x, snowflakeAccountIdentifier: 'zz99999' }),
      (x) => ({ ...x, snowflakeWarehouse: 'OTHER_WH' }),
      (x) => ({ ...x, snowflakeDatabase: 'OTHER_DB' }),
      (x) => ({ ...x, snowflakeSchema: 'OTHER' }),
      (x) => ({ ...x, snowflakeRole: 'ANALYST' }),
      (x) => ({ ...x, snowflakeUser: 'other-user' }),
    ],
  };

  for (const [id, muts] of Object.entries(mutations)) {
    const before = configFingerprint(s, id);
    assert.equal(before, configFingerprint(baseSettings(), id), `${id} fingerprint must be deterministic`);
    muts.forEach((mutate, i) => {
      assert.notEqual(configFingerprint(mutate(s), id), before, `${id} mutation #${i} must change the fingerprint`);
    });
  }

  // Health bookkeeping and enable toggles never affect fingerprints.
  const healthChanged = {
    ...s,
    github: { ...s.github, enabled: false, lastCheckedAt: 999, lastCheckOk: false, lastCheckMessage: 'x', lastVerifiedFingerprint: 'old' },
    dataSources: {
      ...s.dataSources,
      trino: { enabled: true, lastCheckedAt: 999, lastCheckOk: false, lastCheckMessage: 'boom', lastVerifiedFingerprint: 'old' },
    },
  };
  assert.equal(configFingerprint(healthChanged, 'github'), configFingerprint(s, 'github'));
  assert.equal(configFingerprint(healthChanged, 'trino'), configFingerprint(s, 'trino'));
});

test('fingerprintMatchesConfig grandfathers missing fingerprints', async () => {
  const { fingerprintMatchesConfig, configFingerprint } = await loadConnectionConfig();
  const s = baseSettings();
  assert.equal(fingerprintMatchesConfig(undefined, s, 'databricks'), true, 'missing fingerprint stays trusted');
  assert.equal(fingerprintMatchesConfig(null, s, 'databricks'), true, 'null fingerprint stays trusted');
  const fp = configFingerprint(s, 'databricks');
  assert.equal(fingerprintMatchesConfig(fp, s, 'databricks'), true);
  assert.equal(fingerprintMatchesConfig(fp, { ...s, databricksWarehouseId: 'wh-2' }, 'databricks'), false);
});

test('connectionConfigDirty flags config edits but not health updates or materialized defaults', async () => {
  const { connectionConfigDirty } = await loadConnectionConfig();
  const s = baseSettings();

  // Health-only changes (what connect flows write) are not "dirty".
  const healthOnly = {
    ...s,
    sharepoint: { ...s.sharepoint, lastCheckedAt: 123, lastCheckOk: true, lastCheckMessage: null, lastVerifiedFingerprint: 'fp' },
  };
  assert.equal(connectionConfigDirty(s, healthOnly, 'sharepoint'), false);

  // Materializing falsy defaults (getter spreads) is not "dirty".
  const materialized = {
    ...s,
    atlassian: {
      ...s.atlassian,
      defaultJiraProjectKey: '',
      allowedProjects: [],
      browserProfileDir: '',
      confluence: { enabled: false, showBrowser: false },
    },
  };
  assert.equal(connectionConfigDirty(s, materialized, 'atlassian'), false);

  // Real config edits are dirty — and only for the connection that owns them.
  const edited = { ...s, atlassian: { ...s.atlassian, jiraBaseUrl: 'https://other.atlassian.net' } };
  assert.equal(connectionConfigDirty(s, edited, 'atlassian'), true);
  assert.equal(connectionConfigDirty(s, edited, 'sharepoint'), false);
});

// ---------------------------------------------------------------------------
// Console invariants: users are never trapped after setup.
// ---------------------------------------------------------------------------

test('manage affordance is reachable in every post-setup state, even while connecting', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  assert.ok(
    mcp.includes("const showChevron = Boolean(hasDetails && stage !== 'setup');"),
    'the chevron must not require stage === active or a healthy connection, and must survive connecting'
  );
  assert.ok(
    !/setDrawerOpen\(false\);\s*\}, \[stage\]\)/.test(mcp),
    'the drawer must not collapse just because the derived stage changed'
  );
  assert.ok(
    !mcp.includes('cx-swap" key='),
    'the action column must not remount (and drop focus) on stage changes'
  );
});

test('connection actions persist scoped slices, never the whole settings draft', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  assert.ok(
    !mcp.includes('persistCurrentSettings'),
    'broad persistCurrentSettings must not exist in connection flows'
  );
  assert.ok(
    mcp.includes('mergeConnectionPatch(savedSettingsRef.current'),
    'persistence must merge only the connection slice onto saved settings'
  );
  assert.ok(
    mcp.includes("await persistConnectionSlice('") || mcp.includes('await persistConnectionSlice(id)'),
    'connect/test flows persist the connection slice before backend calls'
  );
});

test('every credential sheet has a danger zone with two-step remove', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  const dangerZones = (mcp.match(/<DangerZone/g) || []).length;
  assert.ok(
    dangerZones >= 8,
    `GitHub, Slack, Atlassian, SharePoint, BigQuery, Databricks, Trino and Snowflake sheets all need a danger zone (found ${dangerZones})`
  );
  assert.ok(
    mcp.includes('const TwoStepRemove') && mcp.includes('Yes, remove'),
    'remove must be a two-step inline confirm'
  );
  assert.ok(
    !mcp.includes('cx-sheet-manage'),
    'the one-off per-sheet remove blocks are replaced by the shared danger zone'
  );
});

test('successful verification records a config fingerprint; validation honors it', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  assert.ok(
    /lastVerifiedFingerprint = patch\.lastCheckOk \? configFingerprint\(/.test(mcp),
    'data-source checks stamp the fingerprint on success and clear it on failure'
  );
  assert.ok(
    mcp.includes("lastVerifiedFingerprint: configFingerprint(localSettingsRef.current, 'github')"),
    'OAuth connect success must record the verified fingerprint'
  );
  assert.ok(
    mcp.includes('fingerprintMatchesConfig('),
    'validated states must require the stored fingerprint to match current config'
  );
  assert.ok(
    mcp.includes('Unverified changes'),
    'a fingerprint mismatch shows an unverified-changes state instead of staying green'
  );
  assert.ok(
    mcp.includes("'Verified · off'") && mcp.includes("'Connected · off'"),
    'the enable stage uses honest verified/connected-but-off labels'
  );
});

test('settings migration preserves verified config fingerprints', () => {
  const migration = sourceAt('services/settingsMigration.ts');
  assert.ok(
    migration.includes('const optionalLastVerifiedFingerprint'),
    'migration needs a shared helper for optional fingerprint fields'
  );
  assert.ok(
    migration.includes("typeof value === 'string' || value === null"),
    'migration should preserve string fingerprints and explicit null clears'
  );
  const uses = (migration.match(/optionalLastVerifiedFingerprint\(raw\)/g) || []).length;
  assert.ok(
    uses >= 5,
    `all credential settings migrators should preserve lastVerifiedFingerprint (found ${uses})`
  );
});

test('the setup window is intent-driven and guards dirty closes', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  assert.ok(
    mcp.includes("type SheetIntent = 'setup' | 'edit' | 'reconnect'"),
    'the sheet model carries an explicit setup/edit/reconnect intent'
  );
  assert.ok(
    mcp.includes("'Save & verify'") && mcp.includes('"Save changes"'),
    'edit intent uses Save changes / Save & verify footers'
  );
  assert.ok(
    mcp.includes('Edit connection'),
    'the drawer exposes an explicit Edit connection action (Reconnect is not the only path)'
  );
  assert.ok(
    mcp.includes('connectionConfigDirty(snapshot, localSettingsRef.current, current.id)'),
    'closing checks the snapshot for unsaved changes'
  );
  assert.ok(
    mcp.includes('Keep editing') && mcp.includes('Discard changes'),
    'a dirty close offers keep-editing/discard instead of silently dropping edits'
  );
  assert.ok(
    mcp.includes('mergeConnectionPatch(prev, current.id, snapshot)'),
    'discard restores only that connection slice'
  );
  assert.ok(
    !mcp.includes('sheetStageRef'),
    'the stage-drift auto-close fuse is gone; success callbacks close the sheet explicitly'
  );
  assert.ok(
    (mcp.match(/closeSheetFor\('/g) || []).length >= 8,
    'connect/test success callbacks close the sheet explicitly'
  );
});

test('busy flows expose cancel affordances', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  for (const handler of [
    'handleAtlassianCancelAuth',
    'handleSharePointCancelAuth',
    'handleGitHubCancelAuth',
    'handleSlackCancelAuth',
    'handleAdcReauthCancel',
  ]) {
    assert.ok(mcp.includes(handler), `${handler} must exist so busy states never trap the user`);
  }
  assert.ok(
    mcp.includes('atlassianCancelledRef') && mcp.includes('sharePointCancelledRef'),
    'cancelling a browser sign-in must also stop the status-driven poll auto-resume'
  );
});

// ---------------------------------------------------------------------------
// Accessibility & secret hygiene.
// ---------------------------------------------------------------------------

test('Field produces programmatic labels and required state for AT', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  assert.ok(
    mcp.includes('<label className="cx-field-label" htmlFor={inputId}>'),
    'Field must render a real <label> wired to its input'
  );
  assert.ok(
    mcp.includes('React.useId()'),
    'Field ids must be generated, not hand-rolled'
  );
  assert.ok(
    mcp.includes('aria-describedby={ariaDescribedBy ?? field?.helpId}'),
    'help text must be exposed via aria-describedby'
  );
  assert.ok(
    mcp.includes('cx-sr-only'),
    'required markers need a screen-reader-visible equivalent, not only an aria-hidden asterisk'
  );
});

test('secret inputs are named, labelled and use new-password autocomplete', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  assert.ok(
    mcp.includes('autoComplete="new-password"'),
    'secret fields must opt out of credential autofill via new-password'
  );
  assert.ok(
    !mcp.includes('autoComplete="off"'),
    'autoComplete="off" is ignored by browsers for password fields — do not rely on it'
  );
  const keypairBlock = mcp.slice(mcp.indexOf('handleSetSnowflakeKeyPair'), mcp.indexOf('handleClearSnowflakeSecret'));
  assert.ok(
    keypairBlock.includes("setSnowflakeKeyPassphrase('')"),
    'the Snowflake key passphrase must be cleared after a successful keypair save'
  );
});

test('escape handling: catalogue swallows Escape; both dialogs ignore IME composition', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  const catalogue = mcp.slice(mcp.indexOf('const CataloguePicker'), mcp.indexOf('// --- DataSource helpers'));
  assert.ok(
    catalogue.includes('e.stopPropagation();') && catalogue.includes("window.addEventListener('keydown', onKey, true)"),
    'CataloguePicker must capture Escape and stop it before the Settings dialog sees it'
  );
  assert.ok(
    (mcp.match(/e\.isComposing \|\| e\.keyCode === 229/g) || []).length >= 2,
    'Escape handlers must ignore in-progress IME composition'
  );
  assert.ok(
    mcp.includes('.cx-sheet-body input:not([disabled])'),
    'sheet initial focus must target the first body control, not the close X'
  );
  assert.ok(
    mcp.includes("document.getElementById('cx-console-root')?.focus?.()"),
    'sheet close needs a focus fallback when the trigger node was detached'
  );
});

test('segmented auth pickers expose radio semantics', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  const radiogroups = (mcp.match(/role="radiogroup"/g) || []).length;
  assert.ok(radiogroups >= 3, `Databricks/Trino/Snowflake auth pickers need radiogroup semantics (found ${radiogroups})`);
  assert.ok((mcp.match(/role="radio"/g) || []).length >= 7, 'each segment must be a radio with aria-checked');
});

test('Databricks OAuth postMessage validates the sender origin', () => {
  const mcp = sourceAt('components/settings/McpIntegrationsTab.tsx');
  assert.ok(
    mcp.includes('event.origin !== window.location.origin'),
    'OAuth codes must only be accepted from this app or loopback origins'
  );
});

test('console CSS meets the batch a11y bar', () => {
  const css = sourceAt('components/settings/connectionsConsole.css');
  assert.ok(
    css.includes('--cx-text-faint: #8c8c93'),
    'faint text token must pass WCAG contrast for its small type sizes'
  );
  assert.ok(
    /\.cx-switch \{[^}]*height: 24px/s.test(css),
    'switch target height must meet the 24px minimum'
  );
  assert.ok(css.includes('.cx-sr-only'), 'sr-only utility must exist for hidden labels');
  assert.ok(css.includes('.cx-sheet-guard'), 'unsaved-changes guard styles must exist');
  assert.ok(css.includes('.cx-danger'), 'danger-zone styles must exist');
});

// ---------------------------------------------------------------------------
// Dialog footer + health toast decisions.
// ---------------------------------------------------------------------------

test('Settings footer is honest on the Connections tab', () => {
  const dialog = sourceAt('components/settings/SettingsDialog.tsx');
  assert.ok(
    dialog.includes("activeTab === 'mcps'"),
    'the Connections tab needs its own footer branch'
  );
  assert.ok(
    dialog.includes('Connection changes apply immediately.'),
    'the footer must say connection changes are immediate instead of offering a fake Cancel'
  );
});

test('launch-cadence re-auth toasts are suppressed for disabled connections only', () => {
  const hook = sourceAt('hooks/useConnectionHealth.ts');
  const lib = sourceAt('lib/connectionHealth.ts');
  assert.ok(
    lib.includes('export function isConnectionEnabledForModel('),
    'model-access enablement must be a shared pure helper'
  );
  assert.ok(
    hook.includes('if (!isConnectionEnabledForModel(settingsRef.current, outcome.id)) return;'),
    'sweep toasts are suppressed when model access is off'
  );
  const suppressIdx = hook.indexOf('isConnectionEnabledForModel(settingsRef.current');
  const okClearIdx = hook.indexOf('alertedRef.current.delete(outcome.id)');
  assert.ok(
    okClearIdx !== -1 && okClearIdx < suppressIdx,
    'successful checks still clear alert state before the suppression filter'
  );
});
