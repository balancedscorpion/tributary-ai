import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('settings defaults and migration include chartEngines feature flags', () => {
  const constants = sourceAt('constants.ts');
  const migration = sourceAt('services/settingsMigration.ts');

  assert.ok(
    constants.includes('chartEngines: {') && constants.includes('echarts: true') && constants.includes('highcharts: false'),
    'DEFAULT_SETTINGS should enable echarts and disable highcharts by default'
  );
  assert.ok(
    migration.includes('chartEngines: {'),
    'settings migration should normalize chartEngines'
  );
});

test('manager and chart engine filter gate tool exposure by chart feature flags', () => {
  const manager = sourceAt('server/internal/chat/optionc/manager.go');
  const filter = sourceAt('server/internal/chat/optionc/chart_engine_filter.go');

  assert.ok(
    manager.includes('filterToolDefinitionsByChartEngines'),
    'manager should filter tool definitions by chart engine flags'
  );
  assert.ok(
    manager.includes('embedChartingGuidance'),
    'manager should embed chart guidance based on enabled engines'
  );
  assert.ok(
    filter.includes('toolGetEchartsCapabilities') && filter.includes('toolGetHighchartsCapabilities'),
    'chart engine filter should handle both capability tools'
  );
  assert.ok(
    filter.includes('shapeDisplayTableDefinitionForChartEngines'),
    'chart engine filter should shape display_table schema for enabled engines'
  );
});
