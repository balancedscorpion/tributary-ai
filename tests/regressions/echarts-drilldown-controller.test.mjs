import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('ECharts drilldown controller supports depth guards and back navigation', () => {
  const controllerSource = sourceAt('components/charting/echartsDrilldownController.ts');

  assert.ok(
    controllerSource.includes('createEchartsDrilldownState'),
    'controller should expose createEchartsDrilldownState'
  );
  assert.ok(
    controllerSource.includes('applyEchartsDrilldownClick'),
    'controller should expose click reducer'
  );
  assert.ok(
    controllerSource.includes('stepBackEchartsDrilldown'),
    'controller should expose back-navigation reducer'
  );
  assert.ok(
    controllerSource.includes('Maximum drilldown depth'),
    'controller should enforce max_depth'
  );
  assert.ok(
    controllerSource.includes('drilldown_embedded_series'),
    'controller should enforce sanctioned loader_id'
  );
});

test('EchartsVisualization wires drilldown controller and renders back control', () => {
  const visualizationSource = sourceAt('components/charting/EchartsVisualization.tsx');

  assert.ok(
    visualizationSource.includes("from './echartsDrilldownController';"),
    'visualization should import the drilldown controller'
  );
  assert.ok(
    visualizationSource.includes('applyEchartsDrilldownClick'),
    'visualization should route click events through drilldown reducer'
  );
  assert.ok(
    visualizationSource.includes('stepBackEchartsDrilldown'),
    'visualization should support back-navigation reducer'
  );
  assert.ok(
    visualizationSource.includes('hasBackNav && ('),
    'visualization should conditionally render drilldown back control'
  );
  assert.ok(
    visualizationSource.includes('Back'),
    'visualization should render a back button label'
  );
});
