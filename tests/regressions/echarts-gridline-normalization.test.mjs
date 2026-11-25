import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('EchartsVisualization normalizes secondary axis gridlines', () => {
  const source = sourceAt('components/charting/EchartsVisualization.tsx');

  assert.ok(
    source.includes('function normalizeMultiAxisGridlines'),
    'EchartsVisualization should normalize multi-axis gridline defaults'
  );
  assert.ok(
    source.includes('splitLine.show = false'),
    'Secondary y-axes should default splitLine.show to false'
  );
  assert.ok(
    source.includes('minorSplitLine.show = false'),
    'Secondary y-axes should default minorSplitLine.show to false'
  );
  assert.ok(
    source.includes('nextAxis.alignTicks = true'),
    'Multi-axis value scales should align ticks unless explicitly configured'
  );
  assert.ok(
    source.includes('const optionWithGridlines = normalizeMultiAxisGridlines'),
    'Rendered option should pass through multi-axis gridline normalization'
  );
});
