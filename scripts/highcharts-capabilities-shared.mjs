import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

export const PROJECT_ROOT = path.resolve(__dirname, '..');

export const OUTPUT_PATHS = [
  path.join(PROJECT_ROOT, 'docs', 'generated', 'highcharts-capabilities.json'),
  path.join(PROJECT_ROOT, 'server', 'internal', 'chat', 'tools', 'generated', 'highcharts-capabilities.json'),
];

const CHART_SPEC_VERSIONS = ['highcharts-plot-spec.v1', 'highcharts-plot-spec.v2'];

const CONSTRUCTORS = {
  core: {
    constructor: 'chart',
    bundle: 'highcharts/highcharts',
  },
  stock: {
    constructor: 'stockChart',
    bundle: 'highcharts/highstock',
  },
  maps: {
    constructor: 'mapChart',
    bundle: 'highcharts/highmaps',
  },
  gantt: {
    constructor: 'ganttChart',
    bundle: 'highcharts/highcharts-gantt',
  },
};

const CURATED_MODULE_CATALOG = {
  'highcharts-more': { import_path: 'highcharts/highcharts-more', depends_on: [] },
  drilldown: { import_path: 'highcharts/modules/drilldown', depends_on: [] },
  heatmap: { import_path: 'highcharts/modules/heatmap', depends_on: [] },
  tilemap: { import_path: 'highcharts/modules/tilemap', depends_on: ['heatmap'] },
  treemap: { import_path: 'highcharts/modules/treemap', depends_on: [] },
  sunburst: { import_path: 'highcharts/modules/sunburst', depends_on: ['treemap'] },
  sankey: { import_path: 'highcharts/modules/sankey', depends_on: [] },
  organization: { import_path: 'highcharts/modules/organization', depends_on: ['sankey'] },
  'dependency-wheel': { import_path: 'highcharts/modules/dependency-wheel', depends_on: ['sankey'] },
  networkgraph: { import_path: 'highcharts/modules/networkgraph', depends_on: [] },
  wordcloud: { import_path: 'highcharts/modules/wordcloud', depends_on: [] },
  venn: { import_path: 'highcharts/modules/venn', depends_on: [] },
  timeline: { import_path: 'highcharts/modules/timeline', depends_on: [] },
  xrange: { import_path: 'highcharts/modules/xrange', depends_on: [] },
  streamgraph: { import_path: 'highcharts/modules/streamgraph', depends_on: ['highcharts-more'] },
  'item-series': { import_path: 'highcharts/modules/item-series', depends_on: [] },
  pareto: { import_path: 'highcharts/modules/pareto', depends_on: ['histogram-bellcurve'] },
  dumbbell: { import_path: 'highcharts/modules/dumbbell', depends_on: ['highcharts-more'] },
  lollipop: { import_path: 'highcharts/modules/lollipop', depends_on: ['dumbbell'] },
  funnel: { import_path: 'highcharts/modules/funnel', depends_on: [] },
  'histogram-bellcurve': { import_path: 'highcharts/modules/histogram-bellcurve', depends_on: [] },
  geoheatmap: { import_path: 'highcharts/modules/geoheatmap', depends_on: ['heatmap'] },
  flowmap: { import_path: 'highcharts/modules/flowmap', depends_on: [] },
  tiledwebmap: { import_path: 'highcharts/modules/tiledwebmap', depends_on: [] },
};

const SUPPORTED_SERIES_TYPES_BY_CONSTRUCTOR = {
  core: [
    'line', 'spline', 'area', 'areaspline', 'column', 'bar', 'scatter', 'pie', 'bubble',
    'heatmap', 'tilemap', 'treemap', 'sunburst', 'sankey', 'organization', 'dependencywheel',
    'networkgraph', 'wordcloud', 'venn', 'timeline', 'xrange', 'streamgraph', 'item',
    'pareto', 'dumbbell', 'lollipop', 'funnel', 'histogram', 'bellcurve',
  ],
  stock: [
    'line', 'spline', 'area', 'areaspline', 'column', 'scatter', 'candlestick', 'ohlc',
    'xrange', 'histogram', 'bellcurve', 'pareto',
  ],
  maps: [
    'map', 'mapline', 'mappoint', 'mapbubble', 'bubble', 'scatter', 'heatmap', 'tilemap',
    'geoheatmap', 'flowmap',
  ],
  gantt: ['gantt', 'xrange', 'line', 'column'],
};

const ALLOWED_OPTION_PATHS_BY_CONSTRUCTOR = {
  core: [
    'chart', 'chart.type', 'chart.zoomType',
    'title', 'title.text', 'subtitle', 'subtitle.text', 'caption', 'caption.text',
    'xAxis', 'xAxis.type', 'xAxis.categories', 'xAxis.min', 'xAxis.max',
    'yAxis', 'yAxis.type', 'yAxis.min', 'yAxis.max', 'yAxis.title.text',
    'zAxis', 'colorAxis',
    'legend', 'tooltip', 'tooltip.shared', 'tooltip.outside', 'tooltip.useHTML',
    'tooltip.formatter_id', 'tooltip.pointFormat', 'tooltip.valueSuffix',
    'plotOptions', 'series', 'series[].type', 'series[].name', 'series[].data',
    'series[].id', 'series[].drilldown', 'series[].custom',
    'drilldown', 'drilldown.series',
    'navigation', 'responsive', 'credits', 'time', 'annotations', 'data', 'accessibility', 'boost',
  ],
  stock: [
    'chart', 'chart.type', 'chart.zoomType',
    'title', 'title.text', 'subtitle', 'subtitle.text',
    'xAxis', 'xAxis.type', 'xAxis.categories', 'xAxis.min', 'xAxis.max',
    'yAxis', 'yAxis.type', 'yAxis.min', 'yAxis.max', 'yAxis.title.text',
    'legend', 'tooltip', 'tooltip.shared', 'tooltip.outside', 'tooltip.useHTML',
    'tooltip.formatter_id', 'tooltip.pointFormat', 'tooltip.valueSuffix',
    'plotOptions', 'series', 'series[].type', 'series[].name', 'series[].data',
    'series[].id', 'series[].drilldown', 'series[].custom',
    'drilldown', 'drilldown.series', 'rangeSelector', 'navigator', 'scrollbar',
    'navigation', 'responsive', 'credits', 'time', 'annotations', 'data', 'accessibility', 'boost',
  ],
  maps: [
    'chart', 'chart.type',
    'title', 'title.text', 'subtitle', 'subtitle.text',
    'legend', 'tooltip', 'tooltip.shared', 'tooltip.outside', 'tooltip.useHTML',
    'tooltip.formatter_id', 'tooltip.pointFormat', 'tooltip.valueSuffix',
    'plotOptions', 'series', 'series[].type', 'series[].name', 'series[].data',
    'series[].mapData', 'series[].joinBy', 'series[].keys', 'series[].custom',
    'mapView', 'mapNavigation', 'colorAxis',
    'drilldown', 'drilldown.series',
    'navigation', 'responsive', 'credits', 'time', 'annotations', 'data', 'accessibility', 'boost',
  ],
  gantt: [
    'chart', 'chart.type',
    'title', 'title.text', 'subtitle', 'subtitle.text',
    'xAxis', 'xAxis.type', 'xAxis.categories', 'xAxis.min', 'xAxis.max',
    'yAxis', 'yAxis.type', 'yAxis.min', 'yAxis.max', 'yAxis.title.text',
    'legend', 'tooltip', 'tooltip.shared', 'tooltip.outside', 'tooltip.useHTML',
    'tooltip.formatter_id', 'tooltip.pointFormat', 'tooltip.valueSuffix',
    'plotOptions', 'series', 'series[].type', 'series[].name', 'series[].data',
    'series[].id', 'series[].drilldown', 'series[].custom',
    'drilldown', 'drilldown.series',
    'navigation', 'responsive', 'credits', 'time', 'annotations', 'data', 'accessibility', 'boost',
  ],
};

function readJSON(filePath) {
  const raw = fs.readFileSync(filePath, 'utf8');
  return JSON.parse(raw);
}

function stableStringify(value) {
  if (Array.isArray(value)) {
    return `[${value.map((entry) => stableStringify(entry)).join(',')}]`;
  }
  if (value && typeof value === 'object') {
    const keys = Object.keys(value).sort();
    return `{${keys.map((key) => `${JSON.stringify(key)}:${stableStringify(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}

function sortedUnique(values) {
  return [...new Set(values.filter(Boolean))].sort();
}

function resolveInstalledModuleCatalog(projectRoot) {
  const out = {};
  for (const [name, meta] of Object.entries(CURATED_MODULE_CATALOG)) {
    const relativeImport = String(meta.import_path).replace(/^highcharts\//, '');
    const jsFile = path.join(projectRoot, 'node_modules', 'highcharts', `${relativeImport}.js`);
    if (!fs.existsSync(jsFile)) {
      continue;
    }
    out[name] = {
      import_path: meta.import_path,
      depends_on: [...meta.depends_on].sort(),
    };
  }
  return out;
}

function groupCallbacksByKind(callbackRegistry) {
  const grouped = {};
  const callbacks = Array.isArray(callbackRegistry?.callbacks) ? callbackRegistry.callbacks : [];
  for (const entry of callbacks) {
    const id = String(entry?.id || '').trim();
    const kind = String(entry?.kind || '').trim();
    if (!id || !kind) continue;
    if (!grouped[kind]) grouped[kind] = [];
    grouped[kind].push(id);
  }
  for (const kind of Object.keys(grouped)) {
    grouped[kind] = sortedUnique(grouped[kind]);
  }
  if (!grouped['drilldown.loader']) {
    grouped['drilldown.loader'] = ['drilldown_embedded_series'];
  }
  return grouped;
}

function mergeOptionPaths() {
  const merged = [];
  for (const paths of Object.values(ALLOWED_OPTION_PATHS_BY_CONSTRUCTOR)) {
    merged.push(...paths);
  }
  return sortedUnique(merged);
}

export function buildHighchartsCapabilitiesSnapshot(projectRoot = PROJECT_ROOT) {
  const highchartsPkgPath = path.join(projectRoot, 'node_modules', 'highcharts', 'package.json');
  const callbackRegistryPath = path.join(projectRoot, 'lib', 'charting', 'highcharts-callback-registry.json');

  if (!fs.existsSync(highchartsPkgPath)) {
    throw new Error('Missing dependency: highcharts is not installed. Run npm install first.');
  }

  const highchartsPkg = readJSON(highchartsPkgPath);
  const callbackRegistry = readJSON(callbackRegistryPath);

  const callbackIDsByKind = groupCallbacksByKind(callbackRegistry);
  const moduleCatalog = resolveInstalledModuleCatalog(projectRoot);

  const supportedSeriesTypesByConstructor = {};
  for (const [constructorKey, seriesTypes] of Object.entries(SUPPORTED_SERIES_TYPES_BY_CONSTRUCTOR)) {
    supportedSeriesTypesByConstructor[constructorKey] = sortedUnique(seriesTypes);
  }

  const supportedSeriesTypesGlobal = sortedUnique(
    Object.values(supportedSeriesTypesByConstructor).flatMap((entry) => entry)
  );

  const constructors = {};
  for (const [key, value] of Object.entries(CONSTRUCTORS)) {
    constructors[key] = {
      constructor: value.constructor,
      bundle: value.bundle,
    };
  }

  const base = {
    schema_version: 'highcharts-capabilities.v2',
    stage: 'stage2-curated-full',
    chart_spec_versions: [...CHART_SPEC_VERSIONS],
    // Keep single-version field for backward compatibility with older consumers.
    chart_spec_version: 'highcharts-plot-spec.v2',
    highcharts_version: String(highchartsPkg?.version || 'unknown'),
    constructors,
    module_catalog: moduleCatalog,
    supported_modules: Object.keys(constructors),
    supported_series_types_by_constructor: supportedSeriesTypesByConstructor,
    // Backward-compatible alias retained for existing Stage-1 readers.
    supported_series_types: supportedSeriesTypesByConstructor,
    supported_series_types_global: supportedSeriesTypesGlobal,
    allowed_option_paths_by_constructor: ALLOWED_OPTION_PATHS_BY_CONSTRUCTOR,
    // Backward-compatible alias retained for existing Stage-1 readers.
    allowed_option_paths: mergeOptionPaths(),
    callback_ids_by_kind: callbackIDsByKind,
    // Backward-compatible alias retained for existing Stage-1 readers.
    callback_ids: callbackIDsByKind['tooltip.formatter'] || [],
  };

  const hash = crypto.createHash('sha256').update(stableStringify(base)).digest('hex').slice(0, 12);

  return {
    ...base,
    capability_version: `hc-cap-v2-${hash}`,
  };
}

export function writeSnapshot(snapshot, outputPaths = OUTPUT_PATHS) {
  const content = `${JSON.stringify(snapshot, null, 2)}\n`;
  for (const outputPath of outputPaths) {
    fs.mkdirSync(path.dirname(outputPath), { recursive: true });
    fs.writeFileSync(outputPath, content, 'utf8');
  }
}

export function readSnapshot(filePath) {
  return readJSON(filePath);
}

export function snapshotDigest(snapshot) {
  return crypto.createHash('sha256').update(stableStringify(snapshot)).digest('hex');
}
