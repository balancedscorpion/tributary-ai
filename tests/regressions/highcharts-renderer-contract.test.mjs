import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('DataVisualization wires HighchartsVisualization through plotSpec', () => {
  const source = sourceAt('components/DataVisualization.tsx');

  assert.ok(
    source.includes("import HighchartsVisualization from '@/components/charting/HighchartsVisualization';"),
    'DataVisualization should import HighchartsVisualization'
  );
  assert.ok(
    source.includes('plotSpec?: unknown;'),
    'DataVisualization props should accept plotSpec'
  );
  assert.ok(
    source.includes('const [stablePlotSpec, setStablePlotSpec] = useState<unknown>(() => (plotSpec ?? null));'),
    'DataVisualization should keep a sticky plotSpec while streaming updates arrive'
  );
  assert.ok(
    source.includes('const activePlotSpec = plotSpec ?? stablePlotSpec;'),
    'DataVisualization should render from activePlotSpec'
  );
  assert.ok(
    source.includes('{hasPlotSpec && ('),
    'DataVisualization should render a Highcharts section when an active plotSpec is present'
  );
  assert.ok(
    source.includes('<HighchartsVisualization plotSpec={activePlotSpec} title={title} className={`${chartHeightClass} w-full`} />'),
    'DataVisualization should pass activePlotSpec and title into HighchartsVisualization'
  );
  assert.ok(
    source.includes('setIsTableOpen((open) => !open)'),
    'DataVisualization should allow toggling the table drawer'
  );
  assert.ok(
    source.includes('setIsChartExpanded((expanded) => !expanded)'),
    'DataVisualization should allow expanding the chart area'
  );
});

test('HighchartsVisualization applies dark theme defaults and stage-2 module loading', () => {
  const source = sourceAt('components/charting/HighchartsVisualization.tsx');

  assert.ok(
    source.includes('HIGHCHARTS_DARK_BASE_OPTIONS'),
    'HighchartsVisualization should define dark theme defaults'
  );
  assert.ok(
    source.includes('withDarkThemeDefaults'),
    'HighchartsVisualization should merge dark defaults with sanitized options'
  );
  assert.ok(
    source.includes('loadHighchartsModules'),
    'HighchartsVisualization should dynamically load constructor and extension modules'
  );
  assert.ok(
    source.includes('resolveHighchartsConstructor'),
    'HighchartsVisualization should route constructor type by module'
  );
  assert.ok(
    source.includes('attachHighchartsDrilldownController'),
    'HighchartsVisualization should attach async drilldown controller'
  );
  assert.ok(
    source.includes('options={themedOptions}'),
    'HighchartsVisualization should render with themed options'
  );
  assert.ok(
    source.includes('containerProps={{ style: { width: \'100%\', maxWidth: \'100%\', height: \'100%\', maxHeight: \'100%\', overflow: \'hidden\' } }}'),
    'HighchartsVisualization container should clip overflow and fill bounds'
  );
  assert.ok(
    source.includes('width: resolvedChartSize.width'),
    'HighchartsVisualization should size chart width from resolved viewport/requested dimensions'
  );
  assert.ok(
    source.includes('height: resolvedChartSize.height'),
    'HighchartsVisualization should size chart height from resolved viewport/requested dimensions'
  );
  assert.ok(
    source.includes('chart.setSize(resolvedChartSize.width, resolvedChartSize.height, false)'),
    'HighchartsVisualization should apply resolved chart dimensions'
  );
  assert.ok(
    source.includes('overflow-auto rounded-md border'),
    'HighchartsVisualization should make the chart viewport scrollable when chart dimensions exceed panel size'
  );
  assert.ok(
    source.includes("minWidth: '100%'"),
    'HighchartsVisualization should keep the chart filling the viewport when no overflow is needed'
  );
  assert.ok(
    source.includes("minHeight: '100%'"),
    'HighchartsVisualization should keep the chart height filling the viewport when no overflow is needed'
  );
  assert.ok(
    source.includes('resolveChartTitle'),
    'HighchartsVisualization should resolve a meaningful fallback chart title'
  );
  assert.ok(
    source.includes('immutable={false}'),
    'HighchartsVisualization should update existing chart instance instead of remounting'
  );
  assert.ok(
    source.includes('updateArgs={[true, true, false]}'),
    'HighchartsVisualization should disable animation during live updates to avoid flicker'
  );
  assert.ok(
    source.includes('incomingSanitized.error'),
    'HighchartsVisualization should surface incoming sanitizer errors safely'
  );
});

test('Artifact panel and type renderers pass metadata.plot_spec into DataVisualization', () => {
  const panelSource = sourceAt('components/ArtifactPanel.tsx');
  const typeRendererSource = sourceAt('components/renderers/type/DataRenderer.tsx');
  const aggregateSource = sourceAt('components/renderers/tool/AggregateCsvRenderer.tsx');

  assert.ok(
    panelSource.includes('plotSpec={artifact.metadata?.plot_spec}'),
    'ArtifactPanel fallback renderer should pass plot_spec to DataVisualization'
  );
  assert.ok(
    typeRendererSource.includes('plotSpec={artifact.metadata?.plot_spec}'),
    'DataRenderer should pass plot_spec to DataVisualization'
  );
  assert.ok(
    aggregateSource.includes('plotSpec={plotSpec}'),
    'AggregateCsvRenderer should pass derived plotSpec to DataVisualization'
  );
});
