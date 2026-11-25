import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('artifact render path forwards chart_engine metadata into DataVisualization', () => {
  const artifactPanel = sourceAt('components/ArtifactPanel.tsx');
  const dataRenderer = sourceAt('components/renderers/type/DataRenderer.tsx');
  const aggregateRenderer = sourceAt('components/renderers/tool/AggregateCsvRenderer.tsx');
  const visualization = sourceAt('components/DataVisualization.tsx');

  assert.ok(
    artifactPanel.includes('chartEngine={artifact.metadata?.chart_engine}'),
    'ArtifactPanel should forward chart_engine metadata'
  );
  assert.ok(
    dataRenderer.includes('chartEngine={artifact.metadata?.chart_engine}'),
    'DataRenderer should forward chart_engine metadata'
  );
  assert.ok(
    aggregateRenderer.includes('chartEngine={chartEngine || undefined}'),
    'AggregateCsvRenderer should forward chart_engine metadata'
  );
  assert.ok(
    visualization.includes("resolvedChartEngine === 'echarts'"),
    'DataVisualization should branch render by chart_engine'
  );
});
