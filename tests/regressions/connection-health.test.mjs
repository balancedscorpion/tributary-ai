import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

// ---------------------------------------------------------------------------
// Freshness policy: launch + 24h strict checks, no background polling.
// ---------------------------------------------------------------------------

test('connection health policy trusts credentials for 24 hours', () => {
  const source = sourceAt('lib/connectionHealth.ts');

  assert.ok(
    source.includes('export const CONNECTION_CHECK_MAX_AGE_MS = 24 * 60 * 60 * 1000'),
    'credential trust window should be exactly 24 hours'
  );
  assert.ok(
    source.includes('export const CONNECTION_HEALTH_REEVAL_INTERVAL_MS = 60 * 60 * 1000'),
    're-evaluation cadence should be hourly (local math only, no network unless due)'
  );
  assert.ok(
    source.includes('export function isCheckDue('),
    'staleness must be a pure exported function'
  );
  assert.ok(
    source.includes('if (!last) return true;'),
    'never-verified connections must always be due'
  );
});

test('useConnectionHealth sweeps at launch and on the hourly re-evaluation only', () => {
  const source = sourceAt('hooks/useConnectionHealth.ts');

  assert.ok(
    source.includes('CONNECTION_HEALTH_LAUNCH_DELAY_MS'),
    'launch sweep should be delayed so it never competes with app startup'
  );
  assert.ok(
    source.includes('window.setInterval') && source.includes('CONNECTION_HEALTH_REEVAL_INTERVAL_MS'),
    'the only timer should be the hourly re-evaluation interval'
  );
  assert.ok(
    source.includes('if (!settingsLoaded) return;'),
    'sweeps must wait for real settings, never run against DEFAULT_SETTINGS'
  );
  assert.ok(
    source.includes('outcomes.push(await verifyConnectionCredentials('),
    'checks must run sequentially, not as a parallel burst'
  );
  assert.ok(
    source.includes('if (outcome.indeterminate) return;'),
    'unreachable-backend outcomes must never alert the user'
  );
  assert.ok(
    source.includes('const nextSettings = applyConnectionCheckOutcomes(settingsRef.current, outcomes)')
    && source.includes('await persistSettingsRef.current(nextSettings)'),
    'successful automatic checks must persist durable health and config fingerprints'
  );
});

test('App wires connection health into settings and chat actions', () => {
  const source = sourceAt('App.tsx');

  assert.ok(
    source.includes('useConnectionHealth({'),
    'App should mount the connection health controller'
  );
  assert.ok(
    source.includes("setSettingsFocus({ tab: 'mcps', connectionId: connectionId ?? null })"),
    'reconnect deep-link should target the Connections tab'
  );
  assert.ok(
    source.includes('openConnections: handleOpenConnections')
    && source.includes('reportConnectionAuthFailure: reportToolAuthFailure'),
    'chat surfaces need openConnections + reportConnectionAuthFailure via ChatActionsContext'
  );
  assert.ok(
    source.includes('focusConnectionId={settingsFocus?.connectionId ?? null}'),
    'SettingsDialog should receive the focused connection for deep-linking'
  );
  assert.ok(
    source.includes('persistSettings: saveSettings'),
    'background health checks need the raw settings save path, not the toast-wrapped dialog save'
  );
});

// ---------------------------------------------------------------------------
// Deep-link: Settings dialog lands on the Connections tab / focused card.
// ---------------------------------------------------------------------------

test('SettingsDialog supports controlled tab + connection focus', () => {
  const source = sourceAt('components/settings/SettingsDialog.tsx');

  assert.ok(
    source.includes('<Tabs value={activeTab} onValueChange={setActiveTab}'),
    'tabs must be controlled so deep-links can select the Connections tab'
  );
  assert.ok(
    source.includes("setActiveTab(initialTab || 'providers')"),
    'reopening the dialog should honor the requested initial tab'
  );
  assert.ok(
    source.includes('focusConnectionId={focusConnectionId}'),
    'the focused connection must be forwarded to the Connections console'
  );
});

test('Connections console opens the focused connection setup window once', () => {
  const source = sourceAt('components/settings/McpIntegrationsTab.tsx');

  assert.ok(
    source.includes('focusHandledRef.current = focusConnectionId'),
    'deep-link focus must be applied exactly once per target'
  );
  assert.ok(
    /openSheet\(\s*focusConnectionId/.test(source),
    'deep-link focus should open the connection setup window with an explicit intent'
  );
  assert.ok(
    source.includes('setFocusNotice('),
    'a deep-link target missing from the catalogue must surface an inline notice, not silently no-op'
  );
});

// ---------------------------------------------------------------------------
// Connections console: freshness on rows + manual strict re-verify.
// ---------------------------------------------------------------------------

test('connection rows surface when credentials were last verified', () => {
  const source = sourceAt('components/settings/McpIntegrationsTab.tsx');

  assert.ok(
    source.includes('· checked {relativeTime(checkedAt)}'),
    'rows should show a "checked Xh ago" freshness hint'
  );
  assert.ok(
    source.includes('checkedAt: githubLastCheckedAt')
    && source.includes('checkedAt: sharePointLastCheckedAt')
    && source.includes('checkedAt: bqState.lastCheckedAt'),
    'credential connections must pass their lastCheckedAt into the row'
  );
});

test('Verify all runs strict checks and folds results into local settings', () => {
  const source = sourceAt('components/settings/McpIntegrationsTab.tsx');

  assert.ok(
    source.includes('const handleVerifyAll = React.useCallback'),
    'the console should expose a manual verify-all action'
  );
  assert.ok(
    source.includes('const nextSettings = applyCheckOutcomes(localSettingsRef.current, outcomes)')
    && source.includes('await persistConnectionSlice(outcome.id)'),
    'check outcomes must update rows immediately and persist the touched connection slices'
  );
  assert.ok(
    source.includes('Re-verify all connection credentials'),
    'the verify-all button needs an accessible label'
  );
});

// ---------------------------------------------------------------------------
// Inline chat remediation for auth-class tool failures.
// ---------------------------------------------------------------------------

test('auth-kind tool errors render an inline reconnect action', () => {
  const banner = sourceAt('components/chat/ConnectionReauthBanner.tsx');
  const block = sourceAt('components/chat/ToolResultBlock.tsx');
  const shell = sourceAt('components/chat/AtlassianToolResult.tsx');

  assert.ok(
    banner.includes("diagnostic?.kind !== 'auth') return null"),
    'the envelope-driven banner must only render for auth-class failures'
  );
  assert.ok(
    banner.includes('openConnections(connectionId ?? undefined)'),
    'the reconnect button must deep-link into the Connections console'
  );
  assert.ok(
    banner.includes('reportConnectionAuthFailure(connectionId, toolCallId)'),
    'rendering an auth failure should report it for strict re-verification'
  );
  assert.ok(
    block.includes("errorDiagnostic?.kind === 'auth' && (")
    && block.includes('<ConnectionReauthBanner'),
    'generic envelope errors must surface the reconnect banner'
  );
  assert.ok(
    shell.includes('<ToolEnvelopeReauth envelope={envelope} />'),
    'integration-branded results (GitHub/Slack/Atlassian) must surface it too'
  );
});

test('strict check outcomes distinguish credential failures from transport failures', () => {
  const source = sourceAt('services/connectionHealthApi.ts');

  assert.ok(
    source.includes('function isIndeterminateStatus(status: number): boolean'),
    'transient/missing check endpoints should have a single indeterminate policy'
  );
  assert.ok(
    source.includes('status === 404 || status === 408 || status === 429 || status >= 500'),
    '404/408/429/5xx check failures must not be treated as bad credentials'
  );
  assert.ok(
    source.includes("if (typeof e?.status === 'number')") && source.includes('indeterminate: true'),
    'typed 4xx auth/config failures remain real verdicts while transport failures are indeterminate'
  );
});
