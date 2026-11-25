import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('usage is a first-class read-only Settings tab outside AppSettings', () => {
  const dialog = sourceAt('components/settings/SettingsDialog.tsx');
  const usage = sourceAt('components/settings/UsageTab.tsx');
  const settingsHook = sourceAt('hooks/useSettings.ts');

  assert.match(dialog, /TabsTrigger value="providers">Providers<[\s\S]{0,100}TabsTrigger value="usage">Usage</);
  assert.match(dialog, /UsageTab active=\{isOpen && activeTab === 'usage'\}/);
  assert.match(dialog, /activeTab === 'usage'[\s\S]{0,100}<Button[^>]*>Close<\/Button>/);
  assert.doesNotMatch(usage, /AppSettings|useSettings|migrateSettings/);
  assert.doesNotMatch(settingsHook, /tokenUsage|usage\/tokens/);
});
test('usage requests are lazy, timezone-aware, and abort stale work', () => {
  const api = sourceAt('services/tokenUsageApi.ts');
  const hook = sourceAt('hooks/useTokenUsage.ts');

  assert.match(api, /\/api\/usage\/tokens\/daily\?\$\{params\.toString\(\)\}/);
  assert.match(api, /timezone: query\.timezone/);
  assert.match(hook, /if \(!active\)/);
  assert.match(hook, /new AbortController\(\)/);
  assert.match(hook, /return \(\) => controller\.abort\(\)/);
  assert.match(hook, /sequence !== requestSequence\.current/);
});

test('usage parser is versioned, strict, and uses canonical connection and surface identities', () => {
  const api = sourceAt('services/tokenUsageApi.ts');

  assert.match(api, /TOKEN_USAGE_SCHEMA_VERSION = 2/);
  assert.match(api, /Number\.isSafeInteger/);
  assert.match(api, /connection_id/);
  assert.doesNotMatch(api, /provider_config_id|providerConfigId/);
  assert.match(api, /operation/);
  assert.match(api, /upstream_request_count/);
  assert.match(api, /planned_api_surface/);
  assert.match(api, /effective_api_surface/);
  assert.match(api, /report_complete/);
  assert.match(api, /dropped_invocation_count/);
  assert.match(api, /totalMeasuredInvocationCount \+ counts\.lowerBoundTotalInvocationCount \+ counts\.missingUsageCount !== counts\.invocationCount/);
});

const usageSurfaceFiles = [
  'components/settings/UsageTab.tsx',
  'components/settings/usage/UsageSummary.tsx',
  'components/settings/usage/UsageQualityBanner.tsx',
  'components/settings/usage/UsageToolbar.tsx',
  'components/settings/usage/UsageChart.tsx',
  'components/settings/usage/UsageTable.tsx',
  'components/settings/usage/UsageStates.tsx',
  'components/settings/usage/TokenFloorValue.tsx',
  'components/settings/usage/usageFormat.ts',
  'components/settings/usage/usageViewModel.ts',
];

test('usage report exposes honest coverage, target filters, health, and tracking caveat', () => {
  const banner = sourceAt('components/settings/usage/UsageQualityBanner.tsx');
  const table = sourceAt('components/settings/usage/UsageTable.tsx');
  const toolbar = sourceAt('components/settings/usage/UsageToolbar.tsx');
  const format = sourceAt('components/settings/usage/usageFormat.ts');
  const states = sourceAt('components/settings/usage/UsageStates.tsx');
  const combined = usageSurfaceFiles.map(sourceAt).join('\n');

  assert.match(banner, /usage-coverage-warning/);
  assert.match(banner, /Token totals may be lower than actual consumption/);
  assert.match(banner, /usage-health-/);
  assert.match(table, /<caption className="sr-only">Recorded daily token usage by provider, model, and operation/);
  assert.match(toolbar, /usage-provider-filter/);
  assert.match(toolbar, /usage-model-filter/);
  assert.match(toolbar, /usage-connection-filter/);
  assert.match(toolbar, /usage-operation-filter/);
  assert.match(toolbar, /usage-surface-filter/);
  assert.match(format, /MAX_USAGE_RANGE_DAYS = 366/);
  assert.match(states, /legacy run totals/);
  assert.doesNotMatch(combined, />Exact usage</);
  assert.doesNotMatch(combined, /Monetary cost|token spend|exact cost/i);
});

test('usage summary leads with one total-tokens hero and an honest composition', () => {
  const summary = sourceAt('components/settings/usage/UsageSummary.tsx');
  const table = sourceAt('components/settings/usage/UsageTable.tsx');
  const tokenFloorValue = sourceAt('components/settings/usage/TokenFloorValue.tsx');
  const viewModel = sourceAt('components/settings/usage/usageViewModel.ts');
  const format = sourceAt('components/settings/usage/usageFormat.ts');

  // Exactly one display-scale number, and it is the recorded total.
  assert.match(summary, /text-4xl[^"]*sm:text-5xl/);
  assert.equal((summary.match(/text-5xl/g) || []).length, 1);
  assert.match(summary, /<TokenFloorValue display=\{totalDisplay\} testId="usage-stat-total-tokens" \/>/);
  // Floor (≥) and unknown (—) marks are self-describing for screen readers.
  assert.match(format, /not recorded/);
  assert.match(format, /at least/);
  assert.match(tokenFloorValue, /tokenValueSrText/);
  assert.match(summary, /TokenFloorValue/);
  assert.match(table, /TokenFloorValue/);
  // Input/output support the hero as a keyed composition, not stat cards.
  assert.match(summary, /data-testid="usage-composition-bar"/);
  assert.match(summary, /usage-stat-input-tokens/);
  assert.match(summary, /usage-stat-output-tokens/);
  // The share denominator can never fabricate more than 100%, shares are
  // suppressed when recorded input+output exceed the recorded total, and
  // integer shares are allocated to sum to 100.
  assert.match(viewModel, /Math\.max\(total \|\| 0, attributed\)/);
  assert.match(viewModel, /contradictsTotal/);
  assert.match(summary, /usage-composition-mismatch/);
  assert.match(viewModel, /allocateShares/);
  // Diagnostics stay one disclosure away instead of competing with the hero.
  assert.match(summary, /usage-more-metrics-trigger/);
  assert.match(summary, /usage-stat-cached-input/);
  assert.match(summary, /usage-stat-upstream-requests/);
});
