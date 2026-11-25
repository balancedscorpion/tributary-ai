import type Highcharts from 'highcharts';
import type {
  HighchartsModule,
  HighchartsPlotSpec,
  HighchartsPlotSpecSchemaVersion,
} from '@/types';
import { getTooltipFormatterByID } from './highchartsCallbackRegistry';
import { normalizeHighchartsExtensionModules } from './HighchartsModuleLoader';
import type { HighchartsAsyncDrilldownConfig } from './highchartsDrilldownController';

const SERIES_BY_MODULE: Record<HighchartsModule, Set<string>> = {
  core: new Set([
    'line', 'spline', 'area', 'areaspline', 'column', 'bar', 'scatter', 'pie', 'bubble',
    'heatmap', 'tilemap', 'treemap', 'sunburst', 'sankey', 'organization', 'dependencywheel',
    'networkgraph', 'wordcloud', 'venn', 'timeline', 'xrange', 'streamgraph', 'item',
    'pareto', 'dumbbell', 'lollipop', 'funnel', 'histogram', 'bellcurve',
  ]),
  stock: new Set([
    'line', 'spline', 'area', 'areaspline', 'column', 'scatter', 'candlestick', 'ohlc',
    'xrange', 'histogram', 'bellcurve', 'pareto',
  ]),
  maps: new Set([
    'map', 'mapline', 'mappoint', 'mapbubble', 'bubble', 'scatter',
    'heatmap', 'tilemap', 'geoheatmap', 'flowmap',
  ]),
  gantt: new Set(['gantt', 'xrange', 'line', 'column']),
};

const ALLOWED_OPTION_KEYS = new Set([
  'chart', 'title', 'subtitle', 'caption',
  'xAxis', 'yAxis', 'zAxis', 'colorAxis',
  'legend', 'tooltip', 'plotOptions', 'series',
  'drilldown', 'rangeSelector', 'navigator', 'scrollbar',
  'mapView', 'mapNavigation',
  'navigation', 'pane', 'responsive', 'credits', 'time', 'annotations', 'data', 'accessibility', 'boost',
]);

type PlainObject = Record<string, any>;

interface SanitizedHighchartsExtensions {
  drilldown_async?: HighchartsAsyncDrilldownConfig;
}

export interface SanitizedHighchartsSpec {
  ok: boolean;
  schemaVersion: HighchartsPlotSpecSchemaVersion;
  module: HighchartsModule;
  modules: string[];
  options: Highcharts.Options;
  warnings: string[];
  extensions?: SanitizedHighchartsExtensions;
  error?: string;
}

function isPlainObject(value: unknown): value is PlainObject {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function looksExecutableString(value: string): boolean {
  const text = value.trim().toLowerCase();
  if (!text) return false;
  return text.includes('function(') || text.includes('=>') || text.includes('<script');
}

function hasExecutablePayload(value: unknown): boolean {
  if (typeof value === 'function') return true;
  if (typeof value === 'string' && looksExecutableString(value)) return true;
  if (Array.isArray(value)) return value.some((entry) => hasExecutablePayload(entry));
  if (isPlainObject(value)) {
    for (const [key, entry] of Object.entries(value)) {
      const lower = key.trim().toLowerCase();
      if (lower === 'formatter' || lower === 'events' || lower === '__proto__' || lower === 'constructor' || lower === 'prototype') {
        return true;
      }
      if (hasExecutablePayload(entry)) return true;
    }
  }
  return false;
}

function containsExternalURL(value: unknown): boolean {
  if (typeof value === 'string') {
    const text = value.trim().toLowerCase();
    if (!text) return false;
    return text.startsWith('http://') || text.startsWith('https://') || text.startsWith('//');
  }
  if (Array.isArray(value)) return value.some((entry) => containsExternalURL(entry));
  if (isPlainObject(value)) return Object.values(value).some((entry) => containsExternalURL(entry));
  return false;
}

function asNumber(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string') {
    const n = Number(value.trim());
    if (Number.isFinite(n)) return n;
  }
  return null;
}

function sanitizeScalar(value: unknown): string | number | boolean | null | undefined {
  if (value === null || value === undefined) return value as null | undefined;
  if (typeof value === 'string' || typeof value === 'boolean') return value;
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  const n = asNumber(value);
  if (n !== null) return n;
  return undefined;
}

function sanitizeAnyValue(value: unknown): any {
  if (Array.isArray(value)) {
    const out = value
      .map((entry) => sanitizeAnyValue(entry))
      .filter((entry) => entry !== undefined);
    return out;
  }
  if (isPlainObject(value)) {
    const out: PlainObject = {};
    for (const [key, entry] of Object.entries(value)) {
      if (key === '__proto__' || key === 'constructor' || key === 'prototype') continue;
      const sanitized = sanitizeAnyValue(entry);
      if (sanitized !== undefined) out[key] = sanitized;
    }
    return out;
  }
  return sanitizeScalar(value);
}

function sanitizeAxisValue(raw: unknown): any {
  if (Array.isArray(raw)) {
    return raw
      .map((entry) => sanitizeAxisValue(entry))
      .filter((entry) => entry !== undefined);
  }
  if (!isPlainObject(raw)) return undefined;

  const out: PlainObject = {};
  const axisType = String(raw.type || '').trim();
  if (axisType && ['linear', 'logarithmic', 'datetime', 'category', 'treegrid'].includes(axisType)) {
    out.type = axisType;
  }
  if (Array.isArray(raw.categories)) {
    const categories = raw.categories
      .map((entry: unknown) => sanitizeScalar(entry))
      .filter((entry: unknown) => entry !== undefined);
    if (categories.length > 0) out.categories = categories;
  }
  const min = asNumber(raw.min);
  if (min !== null) out.min = min;
  const max = asNumber(raw.max);
  if (max !== null) out.max = max;
  if (typeof raw.opposite === 'boolean') out.opposite = raw.opposite;
  if (isPlainObject(raw.title)) {
    const titleText = String(raw.title.text || '').trim();
    if (titleText) out.title = { text: titleText };
  }
  return out;
}

function sanitizeSeriesDataPoint(point: unknown): any {
  if (Array.isArray(point)) {
    if (point.length === 0 || point.length > 16) return undefined;
    const values = point
      .map((entry) => sanitizeSeriesDataPoint(entry))
      .filter((entry) => entry !== undefined);
    return values.length === point.length ? values : undefined;
  }
  if (isPlainObject(point)) {
    const out: PlainObject = {};
    for (const [key, value] of Object.entries(point)) {
      if (key === '__proto__' || key === 'constructor' || key === 'prototype') continue;
      if ((key === 'url' || key === 'href') && containsExternalURL(value)) {
        continue;
      }
      const sanitized = sanitizeSeriesDataPoint(value);
      if (sanitized !== undefined) out[key] = sanitized;
    }
    return out;
  }
  return sanitizeScalar(point);
}

function sanitizeChartTextOption(raw: unknown): { text: string } | undefined {
  if (typeof raw === 'string') {
    const text = raw.trim();
    return text ? { text } : undefined;
  }
  if (!isPlainObject(raw)) return undefined;
  const text = String(raw.text || '').trim();
  if (!text) return undefined;
  return { text };
}

function sanitizeSeries(rawSeries: unknown, module: HighchartsModule, warnings: string[]): any[] {
  if (!Array.isArray(rawSeries)) return [];
  const allowedTypes = SERIES_BY_MODULE[module];

  const out: any[] = [];
  for (const entry of rawSeries) {
    if (!isPlainObject(entry)) continue;
    const type = String(entry.type || '').trim().toLowerCase();
    if (!allowedTypes.has(type)) {
      warnings.push(`Unsupported series type "${type}" for module ${module}.`);
      continue;
    }

    const seriesOut: PlainObject = { type };
    if (typeof entry.name === 'string' && entry.name.trim()) {
      seriesOut.name = entry.name.trim();
    }

    if (Array.isArray(entry.data)) {
      const data = entry.data
        .map((point: unknown) => sanitizeSeriesDataPoint(point))
        .filter((point: unknown) => point !== undefined);
      if (data.length > 0) {
        seriesOut.data = data;
      }
    }

    if (Object.prototype.hasOwnProperty.call(entry, 'mapData')) {
      const mapData = (entry as any).mapData;
      if (typeof mapData === 'string') {
        warnings.push('series.mapData string values are not allowed. Embed GeoJSON/TopoJSON inline.');
        continue;
      }
      if (containsExternalURL(mapData)) {
        warnings.push('series.mapData cannot contain external URLs.');
        continue;
      }
      const sanitizedMapData = sanitizeAnyValue(mapData);
      if (sanitizedMapData !== undefined) {
        seriesOut.mapData = sanitizedMapData;
      }
    }

    if (!seriesOut.data && !seriesOut.mapData) {
      warnings.push(`Series "${type}" is missing data.`);
      continue;
    }

    for (const [key, value] of Object.entries(entry)) {
      if (key === 'type' || key === 'name' || key === 'data' || key === 'mapData') continue;
      const sanitized = sanitizeAnyValue(value);
      if (sanitized !== undefined) {
        seriesOut[key] = sanitized;
      }
    }

    out.push(seriesOut);
  }

  return out;
}

function sanitizeTooltip(raw: unknown, warnings: string[]): Highcharts.TooltipOptions {
  if (!isPlainObject(raw)) return { useHTML: false };

  const out: Highcharts.TooltipOptions = {
    useHTML: false,
  };

  if (typeof raw.shared === 'boolean') out.shared = raw.shared;
  if (typeof raw.outside === 'boolean') out.outside = raw.outside;

  const pointFormat = String(raw.pointFormat || '').trim();
  if (pointFormat) out.pointFormat = pointFormat;

  const valueSuffix = String(raw.valueSuffix || '').trim();
  if (valueSuffix) out.valueSuffix = valueSuffix;

  const formatterID = String((raw as any).formatter_id || '').trim();
  if (formatterID) {
    const formatter = getTooltipFormatterByID(formatterID);
    if (formatter) {
      out.formatter = formatter;
    } else {
      warnings.push(`Unsupported tooltip.formatter_id: ${formatterID}`);
    }
  }

  if ((raw as any).useHTML === true) {
    warnings.push('tooltip.useHTML was forced to false for safety.');
  }

  if (Object.prototype.hasOwnProperty.call(raw, 'formatter')) {
    warnings.push('tooltip.formatter was ignored. Use formatter_id instead.');
  }

  return out;
}

function clampDepth(value: unknown): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return 4;
  if (parsed < 1) return 1;
  if (parsed > 8) return 8;
  return Math.trunc(parsed);
}

function sanitizeDrilldownAsync(
  module: HighchartsModule,
  raw: unknown,
  warnings: string[],
): HighchartsAsyncDrilldownConfig | undefined {
  if (!isPlainObject(raw)) {
    warnings.push('extensions.drilldown_async must be an object.');
    return undefined;
  }

  const loaderID = String(raw.loader_id || '').trim();
  if (!loaderID) {
    warnings.push('extensions.drilldown_async.loader_id is required.');
    return undefined;
  }

  const rawSeriesByID = raw.series_by_id;
  if (!isPlainObject(rawSeriesByID)) {
    warnings.push('extensions.drilldown_async.series_by_id must be an object.');
    return undefined;
  }

  const sanitizedSeriesByID: Record<string, Record<string, any>> = {};
  for (const [seriesID, seriesSpec] of Object.entries(rawSeriesByID)) {
    const id = String(seriesID || '').trim();
    if (!id) continue;

    const series = sanitizeSeries([seriesSpec], module, warnings);
    if (series.length === 0) {
      warnings.push(`extensions.drilldown_async.series_by_id[${id}] is invalid.`);
      continue;
    }

    sanitizedSeriesByID[id] = series[0];
  }

  if (Object.keys(sanitizedSeriesByID).length === 0) {
    warnings.push('extensions.drilldown_async.series_by_id has no valid series.');
    return undefined;
  }

  return {
    loader_id: loaderID,
    series_by_id: sanitizedSeriesByID,
    max_depth: clampDepth(raw.max_depth),
  };
}

function sanitizeExtensions(
  spec: PlainObject,
  module: HighchartsModule,
  warnings: string[],
): SanitizedHighchartsExtensions | undefined {
  const extensions = spec.extensions;
  if (!isPlainObject(extensions)) return undefined;

  const out: SanitizedHighchartsExtensions = {};
  if (Object.prototype.hasOwnProperty.call(extensions, 'drilldown_async')) {
    const drilldownAsync = sanitizeDrilldownAsync(module, extensions.drilldown_async, warnings);
    if (drilldownAsync) {
      out.drilldown_async = drilldownAsync;
    }
  }

  return Object.keys(out).length > 0 ? out : undefined;
}

function isValidSchemaVersion(value: string): value is HighchartsPlotSpecSchemaVersion {
  return value === 'highcharts-plot-spec.v1' || value === 'highcharts-plot-spec.v2';
}

function isSupportedModuleForSchema(schemaVersion: HighchartsPlotSpecSchemaVersion, module: string): module is HighchartsModule {
  if (schemaVersion === 'highcharts-plot-spec.v1') {
    return module === 'core' || module === 'stock';
  }
  return module === 'core' || module === 'stock' || module === 'maps' || module === 'gantt';
}

export function sanitizeHighchartsPlotSpec(spec: unknown): SanitizedHighchartsSpec {
  const empty: SanitizedHighchartsSpec = {
    ok: false,
    schemaVersion: 'highcharts-plot-spec.v1',
    module: 'core',
    modules: [],
    options: {},
    warnings: [],
    error: 'Invalid chart spec',
  };

  if (!isPlainObject(spec)) {
    return { ...empty, error: 'chart spec must be an object' };
  }

  const schemaVersionRaw = String((spec as any).schema_version || '').trim();
  if (!isValidSchemaVersion(schemaVersionRaw)) {
    return { ...empty, error: 'Unsupported chart schema_version' };
  }
  const schemaVersion = schemaVersionRaw;

  const moduleValue = String((spec as any).module || '').trim().toLowerCase();
  if (!isSupportedModuleForSchema(schemaVersion, moduleValue)) {
    return { ...empty, schemaVersion, error: `chart module is not supported for ${schemaVersion}` };
  }
  const module = moduleValue as HighchartsModule;

  const optionsRaw = (spec as HighchartsPlotSpec).options;
  if (!isPlainObject(optionsRaw)) {
    return { ...empty, schemaVersion, module, error: 'chart options must be an object' };
  }

  if (hasExecutablePayload(optionsRaw)) {
    return { ...empty, schemaVersion, module, error: 'Executable chart payload is not allowed' };
  }

  const warnings: string[] = [];
  const options: Highcharts.Options = {};

  for (const [key, value] of Object.entries(optionsRaw)) {
    if (!ALLOWED_OPTION_KEYS.has(key)) {
      warnings.push(`Unsupported options key ignored: ${key}`);
      continue;
    }

    if (key === 'title' || key === 'subtitle' || key === 'caption') {
      const textOption = sanitizeChartTextOption(value);
      if (textOption) {
        (options as any)[key] = textOption;
      }
      continue;
    }

    if (key === 'series') {
      const series = sanitizeSeries(value, module, warnings);
      if (series.length === 0) {
        return { ...empty, schemaVersion, module, warnings, error: 'series is missing or unsupported for this module' };
      }
      options.series = series as Highcharts.SeriesOptionsType[];
      continue;
    }

    if (key === 'tooltip') {
      options.tooltip = sanitizeTooltip(value, warnings);
      continue;
    }

    if (key === 'xAxis' || key === 'yAxis') {
      const axis = sanitizeAxisValue(value);
      if (axis !== undefined) {
        (options as any)[key] = axis;
      }
      continue;
    }

    const sanitized = sanitizeAnyValue(value);
    if (sanitized !== undefined) {
      (options as any)[key] = sanitized;
    }
  }

  if (!options.series || options.series.length === 0) {
    return { ...empty, schemaVersion, module, warnings, error: 'series is required' };
  }

  if (module === 'maps') {
    if (containsExternalURL(options)) {
      return { ...empty, schemaVersion, module, warnings, error: 'maps require inline geometry; external URLs are not allowed' };
    }
    const mapRef = isPlainObject((options as any).chart) ? (options as any).chart.map : undefined;
    if (typeof mapRef === 'string' && mapRef.trim()) {
      return { ...empty, schemaVersion, module, warnings, error: 'options.chart.map string references are not allowed; use series.mapData' };
    }
  }

  const modules = normalizeHighchartsExtensionModules((spec as any).modules);
  const extensions = sanitizeExtensions(spec as PlainObject, module, warnings);

  return {
    ok: true,
    schemaVersion,
    module,
    modules,
    options,
    warnings,
    ...(extensions ? { extensions } : {}),
  };
}
