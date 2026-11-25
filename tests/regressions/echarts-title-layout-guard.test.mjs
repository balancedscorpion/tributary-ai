import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('EchartsVisualization constrains long titles to chart bounds', () => {
  const source = sourceAt('components/charting/EchartsVisualization.tsx');

  assert.ok(
    source.includes('function normalizeTitleLayout'),
    'EchartsVisualization should normalize title layout'
  );
  assert.ok(
    source.includes("titleEntry.left = 12"),
    'Title should reserve left padding when model omits positioning'
  );
  assert.ok(
    source.includes("textStyle.overflow = 'truncate'"),
    'Title should default to truncation instead of clipping'
  );
  assert.ok(
    source.includes('textStyle.width = availableWidth'),
    'Title text width should be bounded by container space'
  );
  assert.ok(
    source.includes('const optionWithTitleLayout = normalizeTitleLayout(optionWithGridlines, containerSize);'),
    'Rendered option should pass through title layout normalization'
  );
});
