import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

function jsonAt(relativePath) {
  const raw = sourceAt(relativePath);
  return JSON.parse(raw);
}

test('ECharts capability snapshot exposes safe-full coverage and callback surfaces', () => {
  const snapshot = jsonAt('docs/generated/echarts-capabilities.json');

  assert.ok(Array.isArray(snapshot.supported_modules), 'supported_modules should be an array');
  assert.ok(snapshot.supported_modules.includes('all'), 'supported_modules should include "all"');

  assert.ok(
    Array.isArray(snapshot.supported_series_types_global) && snapshot.supported_series_types_global.length > 0,
    'supported_series_types_global should be populated'
  );
  assert.ok(snapshot.supported_series_types_global.includes('custom'), 'global series should include custom');
  assert.ok(snapshot.supported_series_types_global.includes('map'), 'global series should include map');
  assert.ok(snapshot.supported_series_types_global.includes('lines'), 'global series should include lines');

  const callbackKinds = snapshot.callback_ids_by_kind || {};
  assert.ok(
    Array.isArray(callbackKinds['series.custom.render_item']) && callbackKinds['series.custom.render_item'].length > 0,
    'callback_ids_by_kind should include series.custom.render_item ids'
  );
});

test('ECharts sanitizer is snapshot-driven and enforces callback-id-only custom rendering', () => {
  const sanitizerSource = sourceAt('components/charting/echartsSpecSanitizer.ts');

  assert.ok(
    sanitizerSource.includes("import echartsCapabilitiesSnapshot from '@/docs/generated/echarts-capabilities.json';"),
    'sanitizer should import generated capabilities snapshot'
  );
  assert.ok(
    sanitizerSource.includes('supported_series_types_by_profile'),
    'sanitizer should read snapshot series profiles'
  );
  assert.ok(
    sanitizerSource.includes('allowed_option_paths_by_profile'),
    'sanitizer should read snapshot option paths'
  );
  assert.ok(
    sanitizerSource.includes('series.custom.render_item'),
    'sanitizer should enforce custom render callback ids'
  );
  assert.ok(
    sanitizerSource.includes('renderItem is not allowed; use render_item_id'),
    'sanitizer should reject raw renderItem callbacks'
  );
});

test('ECharts callback registry exposes custom series render callbacks by ID', () => {
  const callbackSource = sourceAt('components/charting/echartsCallbackRegistry.ts');

  assert.ok(
    callbackSource.includes('custom_render_rect'),
    'callback registry should include custom_render_rect'
  );
  assert.ok(
    callbackSource.includes('getEchartsSeriesRenderItemByID'),
    'callback registry should expose custom render callback resolver'
  );
});
