import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('EchartsVisualization reserves grid space for legend to prevent overlap', () => {
  const source = sourceAt('components/charting/EchartsVisualization.tsx');

  assert.ok(
    source.includes('function normalizeLegendAndGridLayout'),
    'EchartsVisualization should normalize legend/grid layout'
  );
  assert.ok(
    source.includes('grid.bottom = Math.max(currentBottom, requiredBottom)'),
    'Bottom legend layout should reserve vertical space in grid.bottom'
  );
  assert.ok(
    source.includes("legend.type = rowsNeeded > 1 ? 'scroll' : 'plain'"),
    'Legend should switch to scroll mode when many items are present'
  );
  assert.ok(
    source.includes('grid.containLabel = true'),
    'Grid should keep labels/title contained after legend normalization'
  );
  assert.ok(
    source.includes('normalizeLegendAndGridLayout(optionWithTitleLayout, containerSize);'),
    'Legend layout normalization should be applied to rendered options'
  );
});
