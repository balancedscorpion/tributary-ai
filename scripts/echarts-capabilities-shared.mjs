import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

export const PROJECT_ROOT = path.resolve(__dirname, '..');

export const OUTPUT_PATHS = [
  path.join(PROJECT_ROOT, 'docs', 'generated', 'echarts-capabilities.json'),
  path.join(PROJECT_ROOT, 'server', 'internal', 'chat', 'tools', 'generated', 'echarts-capabilities.json'),
];

const CHART_SPEC_VERSIONS = ['echarts-plot-spec.v1'];
const SUPPORTED_MODULE_PROFILES = ['core', 'dataviz', 'graph', 'all'];

const LEGACY_SUPPORTED_SERIES_TYPES_BY_PROFILE = {
  core: ['line', 'bar', 'scatter', 'pie', 'radar', 'funnel', 'gauge', 'candlestick', 'boxplot'],
  dataviz: ['heatmap', 'treemap', 'sunburst', 'sankey', 'parallel', 'themeRiver'],
  graph: ['graph', 'tree'],
};

const LEGACY_ALLOWED_OPTION_PATHS_BY_PROFILE = {
  core: [
    'title', 'title.text', 'legend', 'tooltip', 'tooltip.trigger', 'tooltip.formatter_id',
    'grid', 'xAxis', 'yAxis', 'dataset', 'series', 'series[].type', 'series[].name',
    'series[].data', 'series[].render_item_id', 'visualMap', 'dataZoom', 'toolbox',
    'animation', 'color', 'backgroundColor', 'textStyle', 'aria', 'timeline', 'graphic',
  ],
  dataviz: [
    'title', 'title.text', 'legend', 'tooltip', 'tooltip.trigger', 'tooltip.formatter_id',
    'grid', 'xAxis', 'yAxis', 'dataset', 'series', 'series[].type', 'series[].name',
    'series[].data', 'series[].render_item_id', 'visualMap', 'dataZoom', 'toolbox',
    'animation', 'color', 'backgroundColor', 'textStyle', 'aria', 'timeline', 'graphic',
    'calendar', 'singleAxis',
  ],
  graph: [
    'title', 'title.text', 'legend', 'tooltip', 'tooltip.trigger', 'tooltip.formatter_id',
    'dataset', 'series', 'series[].type', 'series[].name', 'series[].data',
    'series[].nodes', 'series[].links', 'series[].render_item_id',
    'visualMap', 'toolbox', 'animation', 'color', 'backgroundColor', 'textStyle',
    'aria', 'timeline', 'graphic',
  ],
};

const SAFE_CUSTOM_SERIES_KEYS = [
  'series[].encode',
  'series[].datasetIndex',
  'series[].dimensions',
  'series[].coordinateSystem',
  'series[].clip',
  'series[].z',
  'series[].zlevel',
  'series[].silent',
  'series[].name',
  'series[].itemStyle',
  'series[].emphasis',
  'series[].render_item_id',
];

const REQUIRED_SAFE_OPTION_PATHS = [
  'tooltip.formatter_id',
  'series[].type',
  'series[].name',
  'series[].data',
];

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

function parseNamedExports(filePath) {
  const source = fs.readFileSync(filePath, 'utf8');
  const out = [];
  const re = /export\s+\{\s*[^}]*\bas\s+([A-Za-z0-9_$]+)\s*\}\s+from/g;
  let match;
  while ((match = re.exec(source)) !== null) {
    out.push(match[1]);
  }
  return sortedUnique(out);
}

function extractBraceBlock(source, startIndex) {
  if (startIndex < 0 || startIndex >= source.length) {
    return null;
  }
  let depth = 0;
  let blockStart = -1;
  for (let i = startIndex; i < source.length; i += 1) {
    const ch = source[i];
    if (ch === '{') {
      depth += 1;
      if (blockStart < 0) {
        blockStart = i;
      }
      continue;
    }
    if (ch === '}') {
      depth -= 1;
      if (depth === 0 && blockStart >= 0) {
        return source.slice(blockStart + 1, i);
      }
      continue;
    }
  }
  return null;
}

function parseInterfacePropertyKeys(source, interfaceName) {
  const marker = `interface ${interfaceName}`;
  const start = source.indexOf(marker);
  if (start < 0) {
    throw new Error(`Unable to locate ${marker}`);
  }
  const block = extractBraceBlock(source, start);
  if (!block) {
    throw new Error(`Unable to parse body for ${marker}`);
  }
  const out = [];
  const re = /^\s*([A-Za-z_$][A-Za-z0-9_$]*)\??\s*:/gm;
  let match;
  while ((match = re.exec(block)) !== null) {
    out.push(match[1]);
  }
  return sortedUnique(out);
}

function parseTypeLiteralPropertyKeys(source, typeName) {
  const marker = `type ${typeName} = {`;
  const start = source.indexOf(marker);
  if (start < 0) {
    throw new Error(`Unable to locate ${marker}`);
  }
  const block = extractBraceBlock(source, start);
  if (!block) {
    throw new Error(`Unable to parse body for ${marker}`);
  }
  const out = [];
  const re = /^\s*([A-Za-z_$][A-Za-z0-9_$]*)\??\s*:/gm;
  let match;
  while ((match = re.exec(block)) !== null) {
    out.push(match[1]);
  }
  return sortedUnique(out);
}

function parseSupportedOptionRoots(sharedTypesPath) {
  const source = fs.readFileSync(sharedTypesPath, 'utf8');
  const echartsOptionRoots = parseInterfacePropertyKeys(source, 'EChartsOption');
  const ecUnitOptionRoots = parseTypeLiteralPropertyKeys(source, 'ECUnitOption');
  return sortedUnique([...echartsOptionRoots, ...ecUnitOptionRoots]);
}

function parseRegisteredSeriesTypes(filePath) {
  const source = fs.readFileSync(filePath, 'utf8');
  const blockMatch = source.match(/interface\s+RegisteredSeriesOption\s*\{([\s\S]*?)\n\}/m);
  if (!blockMatch) {
    throw new Error(`Unable to locate RegisteredSeriesOption in ${filePath}`);
  }
  const block = blockMatch[1];
  const out = [];
  const re = /^\s*([A-Za-z0-9_$]+)\s*:/gm;
  let match;
  while ((match = re.exec(block)) !== null) {
    out.push(match[1]);
  }
  return sortedUnique(out);
}

function chartExportToSeriesType(chartExportName) {
  if (!chartExportName.endsWith('Chart')) {
    return null;
  }
  const stem = chartExportName.slice(0, -'Chart'.length);
  if (!stem) {
    return null;
  }
  return stem[0].toLowerCase() + stem.slice(1);
}

function mapChartExportsToSeriesTypes(chartExports, registeredSeriesTypes) {
  const registeredSet = new Set(registeredSeriesTypes);
  const mapping = {};
  for (const chartExport of chartExports) {
    const mapped = chartExportToSeriesType(chartExport);
    if (!mapped || !registeredSet.has(mapped)) {
      throw new Error(
        `Unable to map installed chart export "${chartExport}" to a registered ECharts series type. `
        + 'Update chartExportToSeriesType mapping logic or installed compatibility assumptions.'
      );
    }
    mapping[chartExport] = mapped;
  }
  return mapping;
}

function mergeOptionPaths(pathsByProfile, allSeriesTypes) {
  const merged = [];
  for (const paths of Object.values(pathsByProfile)) {
    merged.push(...paths);
  }
  const allPaths = sortedUnique(merged);

  // Ensure custom-series-safe keys are present whenever custom series is supported.
  if (allSeriesTypes.includes('custom')) {
    return sortedUnique([...allPaths, ...SAFE_CUSTOM_SERIES_KEYS]);
  }
  return allPaths;
}

function buildSeriesTypesByProfile(registeredSeriesTypes) {
  const out = {};
  for (const profile of SUPPORTED_MODULE_PROFILES) {
    if (profile === 'all') {
      out.all = [...registeredSeriesTypes];
      continue;
    }
    const curated = LEGACY_SUPPORTED_SERIES_TYPES_BY_PROFILE[profile] || [];
    out[profile] = curated.filter((seriesType) => registeredSeriesTypes.includes(seriesType));
  }
  return out;
}

function buildAllowedOptionPathsByProfile(seriesTypesByProfile, supportedOptionRoots) {
  const rootPaths = Array.isArray(supportedOptionRoots)
    ? supportedOptionRoots.filter((entry) => typeof entry === 'string' && entry.trim().length > 0)
    : [];
  const out = {};
  for (const profile of SUPPORTED_MODULE_PROFILES) {
    let basePaths;
    if (profile === 'all') {
      basePaths = mergeOptionPaths(LEGACY_ALLOWED_OPTION_PATHS_BY_PROFILE, seriesTypesByProfile.all || []);
    } else {
      basePaths = LEGACY_ALLOWED_OPTION_PATHS_BY_PROFILE[profile] || [];
    }
    const withRoots = sortedUnique([...basePaths, ...rootPaths, ...REQUIRED_SAFE_OPTION_PATHS]);
    const withCustom = (seriesTypesByProfile[profile] || []).includes('custom')
      ? sortedUnique([...withRoots, ...SAFE_CUSTOM_SERIES_KEYS])
      : withRoots;
    out[profile] = withCustom;
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
    const loweredKind = kind.toLowerCase();
    if (!grouped[loweredKind]) grouped[loweredKind] = [];
    grouped[loweredKind].push(id);
  }
  for (const kind of Object.keys(grouped)) {
    grouped[kind] = sortedUnique(grouped[kind]);
  }
  if (!grouped['drilldown.loader']) {
    grouped['drilldown.loader'] = ['drilldown_embedded_series'];
  }
  if (!grouped['series.custom.render_item']) {
    grouped['series.custom.render_item'] = ['custom_render_rect'];
  }
  return grouped;
}

function resolveInstalledExtensionModules(projectRoot, echartsPkg) {
  const out = new Set();

  const extensionRoot = path.join(projectRoot, 'node_modules', 'echarts', 'extension');
  if (fs.existsSync(extensionRoot) && fs.statSync(extensionRoot).isDirectory()) {
    const entries = fs.readdirSync(extensionRoot, { withFileTypes: true });
    for (const entry of entries) {
      if (!entry.name) continue;
      if (entry.isDirectory()) {
        out.add(entry.name.toLowerCase());
      } else if (entry.isFile() && entry.name.endsWith('.js')) {
        out.add(entry.name.replace(/\.js$/i, '').toLowerCase());
      }
    }
  }

  const exportsField = echartsPkg && typeof echartsPkg === 'object' ? echartsPkg.exports : null;
  if (exportsField && typeof exportsField === 'object') {
    for (const key of Object.keys(exportsField)) {
      if (!key.startsWith('./extension/')) continue;
      const tail = key.slice('./extension/'.length);
      const firstSegment = tail.split('/')[0].replace(/\.js$/i, '').trim();
      if (firstSegment) {
        out.add(firstSegment.toLowerCase());
      }
    }
  }

  return [...out].sort();
}

export function buildEchartsCapabilitiesSnapshot(projectRoot = PROJECT_ROOT) {
  const echartsPkgPath = path.join(projectRoot, 'node_modules', 'echarts', 'package.json');
  const callbackRegistryPath = path.join(projectRoot, 'lib', 'charting', 'echarts-callback-registry.json');
  const chartExportsPath = path.join(projectRoot, 'node_modules', 'echarts', 'lib', 'export', 'charts.js');
  const componentExportsPath = path.join(projectRoot, 'node_modules', 'echarts', 'lib', 'export', 'components.js');
  const featureExportsPath = path.join(projectRoot, 'node_modules', 'echarts', 'lib', 'export', 'features.js');
  const sharedTypesPath = path.join(projectRoot, 'node_modules', 'echarts', 'types', 'dist', 'shared.d.ts');

  if (!fs.existsSync(echartsPkgPath)) {
    throw new Error('Missing dependency: echarts is not installed. Run npm install first.');
  }
  if (!fs.existsSync(chartExportsPath) || !fs.existsSync(componentExportsPath) || !fs.existsSync(featureExportsPath)) {
    throw new Error('Missing ECharts export metadata files under node_modules/echarts/lib/export.');
  }
  if (!fs.existsSync(sharedTypesPath)) {
    throw new Error('Missing ECharts shared type declarations at node_modules/echarts/types/dist/shared.d.ts.');
  }

  const echartsPkg = readJSON(echartsPkgPath);
  const callbackRegistry = readJSON(callbackRegistryPath);

  const supportedChartExports = parseNamedExports(chartExportsPath);
  const supportedComponentExports = parseNamedExports(componentExportsPath);
  const supportedFeatureExports = parseNamedExports(featureExportsPath);
  const registeredSeriesTypes = parseRegisteredSeriesTypes(sharedTypesPath);
  const supportedOptionRoots = parseSupportedOptionRoots(sharedTypesPath);

  const chartExportSeriesMap = mapChartExportsToSeriesTypes(supportedChartExports, registeredSeriesTypes);
  const mappedSeriesTypes = sortedUnique(Object.values(chartExportSeriesMap));
  const callbackIDsByKind = groupCallbacksByKind(callbackRegistry);
  const extensionModules = resolveInstalledExtensionModules(projectRoot, echartsPkg);

  const supportedSeriesTypesByProfile = buildSeriesTypesByProfile(registeredSeriesTypes);
  const allowedOptionPathsByProfile = buildAllowedOptionPathsByProfile(
    supportedSeriesTypesByProfile,
    supportedOptionRoots,
  );
  const allAllowedOptionPaths = mergeOptionPaths(allowedOptionPathsByProfile, registeredSeriesTypes);

  const base = {
    schema_version: 'echarts-capabilities.v1',
    stage: 'stage2-safe-full',
    chart_spec_versions: [...CHART_SPEC_VERSIONS],
    chart_spec_version: 'echarts-plot-spec.v1',
    echarts_version: String(echartsPkg?.version || 'unknown'),

    // Backward-compatible key; now includes the new profile "all".
    supported_modules: [...SUPPORTED_MODULE_PROFILES],

    supported_chart_exports: supportedChartExports,
    supported_component_exports: supportedComponentExports,
    supported_feature_exports: supportedFeatureExports,
    chart_export_to_series_type: chartExportSeriesMap,

    // Backward-compatible key retained.
    supported_series_types_by_module: supportedSeriesTypesByProfile,
    supported_series_types_by_profile: supportedSeriesTypesByProfile,
    supported_series_types_global: [...registeredSeriesTypes],
    supported_series_types_from_chart_exports: mappedSeriesTypes,

    supported_option_roots: supportedOptionRoots,

    // Backward-compatible key retained.
    allowed_option_paths_by_module: allowedOptionPathsByProfile,
    allowed_option_paths_by_profile: allowedOptionPathsByProfile,
    allowed_option_paths: allAllowedOptionPaths,

    supported_extension_modules: extensionModules,

    callback_ids_by_kind: callbackIDsByKind,
    callback_ids: callbackIDsByKind['tooltip.formatter'] || [],
  };

  const hash = crypto.createHash('sha256').update(stableStringify(base)).digest('hex').slice(0, 12);
  return {
    ...base,
    capability_version: `ec-cap-v1-${hash}`,
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
