import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('inline chart preview supports both highcharts and echarts plot specs', () => {
  const previewSource = sourceAt('components/chat/InlineChartPreview.tsx');

  assert.ok(
    previewSource.includes('sanitizeEchartsPlotSpec'),
    'InlineChartPreview should keep ECharts sanitization support'
  );
  assert.ok(
    previewSource.includes('sanitizeHighchartsPlotSpec'),
    'InlineChartPreview should sanitize Highcharts specs for display_table previews'
  );
  assert.ok(
    previewSource.includes('loadHighchartsModules'),
    'InlineChartPreview should load required Highcharts modules before rendering previews'
  );
  assert.ok(
    previewSource.includes('HighchartsReact'),
    'InlineChartPreview should render Highcharts previews when a Highcharts plot spec is provided'
  );
});
