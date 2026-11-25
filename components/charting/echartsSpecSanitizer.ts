import type { EChartsOption } from 'echarts';
import type { EchartsModule } from '@/types';
import echartsCapabilitiesSnapshot from '@/docs/generated/echarts-capabilities.json';
import {
  getEchartsSeriesRenderItemByID,
  getEchartsTooltipFormatterByID,
} from './echartsCallbackRegistry';

type PlainObject = Record<string, any>;
const NUMERIC_LIKE_RE = /^[+-]?(?:\d+\.?\d*|\d*\.?\d+)(?:[eE][+-]?\d+)?$/;

interface EchartsCapabilitiesSnapshot {
  supported_modules?: unknown;
  supported_series_types_by_profile?: unknown;
  supported_series_types_by_module?: unknown;
  allowed_option_paths_by_profile?: unknown;
  allowed_option_paths_by_module?: unknown;
  callback_ids_by_kind?: unknown;
  supported_extension_modules?: unknown;
}

const snapshot = echartsCapabilitiesSnapshot as EchartsCapabilitiesSnapshot;

const FALLBACK_SUPPORTED_MODULES = ['core', 'dataviz', 'graph', 'all'] as const;
const FALLBACK_SERIES_TYPES_BY_MODULE: Record<string, string[]> = {
  core: ['line', 'bar', 'scatter', 'pie', 'radar', 'funnel', 'gauge', 'candlestick', 'boxplot'],
  dataviz: ['heatmap', 'treemap', 'sunburst', 'sankey', 'parallel', 'themeRiver'],
  graph: ['graph', 'tree'],
  all: [
    'bar',
    'boxplot',
    'candlestick',
    'chord',
    'custom',
    'effectScatter',
    'funnel',
    'gauge',
    'graph',
    'heatmap',
    'line',
    'lines',
    'map',
    'parallel',
    'pictorialBar',
    'pie',
    'radar',
    'sankey',
    'scatter',
    'sunburst',
    'themeRiver',
    'tree',
    'treemap',
  ],
};
const FALLBACK_OPTION_ROOTS = [
  'angleAxis',
  'animation',
  'aria',
  'axisPointer',
  'backgroundColor',
  'baseOption',
  'brush',
  'calendar',
  'color',
  'darkMode',
  'dataZoom',
  'dataset',
  'geo',
  'graphic',
  'grid',
  'hoverLayerThreshold',
  'legacyViewCoordSysCenterBase',
  'legend',
  'matrix',
  'media',
  'options',
  'parallel',
  'parallelAxis',
  'polar',
  'radar',
  'radiusAxis',
  'series',
  'singleAxis',
  'stateAnimation',
  'textStyle',
  'thumbnail',
  'timeline',
  'title',
  'toolbox',
  'tooltip',
  'useUTC',
  'visualMap',
  'xAxis',
  'yAxis',
];
const FALLBACK_CALLBACKS_BY_KIND: Record<string, string[]> = {
  'tooltip.formatter': [
    'echarts_tooltip_compact',
    'echarts_tooltip_currency',
    'echarts_tooltip_percent_1dp',
  ],
  'drilldown.loader': ['drilldown_embedded_series'],
  'series.custom.render_item': ['custom_render_rect'],
};

export interface SanitizedEchartsSpec {
  ok: boolean;
  schemaVersion: 'echarts-plot-spec.v1';
  module: EchartsModule;
  modules: string[];
  options: EChartsOption;
  warnings: string[];
  extensions?: {
    drilldown_async?: {
      loader_id: string;
      series_by_id: Record<string, Record<string, any>>;
      max_depth: number;
    };
  };
  error?: string;
}

function toStringArray(input: unknown, lower = true): string[] {
  if (!Array.isArray(input)) return [];
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of input) {
    const value = String(raw ?? '').trim();
    if (!value) continue;
    const next = lower ? value.toLowerCase() : value;
    if (seen.has(next)) continue;
    seen.add(next);
    out.push(next);
  }
  return out;
}

function normalizeMapOfStringArrays(input: unknown): Record<string, Set<string>> {
  if (!input || typeof input !== 'object' || Array.isArray(input)) {
    return {};
  }
  const out: Record<string, Set<string>> = {};
  for (const [key, value] of Object.entries(input as Record<string, unknown>)) {
    const normalizedKey = key.trim().toLowerCase();
    if (!normalizedKey) continue;
    const values = toStringArray(value, true);
    if (values.length === 0) continue;
    out[normalizedKey] = new Set(values);
  }
  return out;
}

function normalizeOptionRootsByProfile(input: unknown): Record<string, Set<string>> {
  if (!input || typeof input !== 'object' || Array.isArray(input)) {
    return {};
  }
  const out: Record<string, Set<string>> = {};
  for (const [key, value] of Object.entries(input as Record<string, unknown>)) {
    const normalizedKey = key.trim().toLowerCase();
    if (!normalizedKey) continue;
    const paths = toStringArray(value, false);
    if (paths.length === 0) continue;
    const roots = new Set<string>();
    for (const path of paths) {
      let root = path.trim();
      const dot = root.indexOf('.');
      if (dot >= 0) root = root.slice(0, dot);
      const bracket = root.indexOf('[');
      if (bracket >= 0) root = root.slice(0, bracket);
      root = root.trim();
      if (root) roots.add(root);
    }
    if (roots.size > 0) out[normalizedKey] = roots;
  }
  return out;
}

function normalizeCallbacksByKind(input: unknown): Record<string, Set<string>> {
  if (!input || typeof input !== 'object' || Array.isArray(input)) {
    return {};
  }
  const out: Record<string, Set<string>> = {};
  for (const [kind, values] of Object.entries(input as Record<string, unknown>)) {
    const normalizedKind = kind.trim().toLowerCase();
    if (!normalizedKind) continue;
    const ids = toStringArray(values, false);
    if (ids.length === 0) continue;
    out[normalizedKind] = new Set(ids);
  }
  return out;
}

function mapFromFallbackSeries(): Record<string, Set<string>> {
  const out: Record<string, Set<string>> = {};
  for (const [module, seriesTypes] of Object.entries(FALLBACK_SERIES_TYPES_BY_MODULE)) {
    out[module] = new Set(seriesTypes);
  }
  return out;
}

function mapFromFallbackOptionRoots(): Record<string, Set<string>> {
  return {
    core: new Set(FALLBACK_OPTION_ROOTS),
    dataviz: new Set(FALLBACK_OPTION_ROOTS),
    graph: new Set(FALLBACK_OPTION_ROOTS),
    all: new Set(FALLBACK_OPTION_ROOTS),
  };
}

function mapFromFallbackCallbacks(): Record<string, Set<string>> {
  const out: Record<string, Set<string>> = {};
  for (const [kind, ids] of Object.entries(FALLBACK_CALLBACKS_BY_KIND)) {
    out[kind] = new Set(ids);
  }
  return out;
}

const SUPPORTED_MODULES = (() => {
  const configured = toStringArray(snapshot.supported_modules, true);
  return new Set(configured.length > 0 ? configured : FALLBACK_SUPPORTED_MODULES);
})();

const SERIES_TYPES_BY_MODULE = (() => {
  const byProfile = normalizeMapOfStringArrays(snapshot.supported_series_types_by_profile);
  const byModule = normalizeMapOfStringArrays(snapshot.supported_series_types_by_module);
  const selected = Object.keys(byProfile).length > 0 ? byProfile : byModule;
  if (Object.keys(selected).length > 0) return selected;
  return mapFromFallbackSeries();
})();

const OPTION_ROOTS_BY_MODULE = (() => {
  const byProfile = normalizeOptionRootsByProfile(snapshot.allowed_option_paths_by_profile);
  const byModule = normalizeOptionRootsByProfile(snapshot.allowed_option_paths_by_module);
  const selected = Object.keys(byProfile).length > 0 ? byProfile : byModule;
  if (Object.keys(selected).length > 0) return selected;
  return mapFromFallbackOptionRoots();
})();

const CALLBACK_IDS_BY_KIND = (() => {
  const parsed = normalizeCallbacksByKind(snapshot.callback_ids_by_kind);
  if (Object.keys(parsed).length > 0) return parsed;
  return mapFromFallbackCallbacks();
})();

const SUPPORTED_EXTENSION_MODULES = (() => {
  const parsed = toStringArray(snapshot.supported_extension_modules, true);
  return new Set(parsed);
})();

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
      if (
        lower === 'formatter'
        || lower === 'events'
        || lower === 'renderitem'
        || lower === '__proto__'
        || lower === 'constructor'
        || lower === 'prototype'
      ) {
        return true;
      }
      if (hasExecutablePayload(entry)) return true;
    }
  }
  return false;
}

function sanitizeScalar(value: unknown): string | number | boolean | null | undefined {
  if (value === null || value === undefined) return value as null | undefined;
  if (typeof value === 'string' || typeof value === 'boolean') return value;
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  return undefined;
}

function coerceNumericLike(value: unknown): unknown {
  if (typeof value !== 'string') return value;
  const trimmed = value.trim();
  if (!trimmed || !NUMERIC_LIKE_RE.test(trimmed)) return value;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? parsed : value;
}

function normalizeCartesianPointValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    if (value.length === 0) return undefined;
    const normalized = value.map((entry) => coerceNumericLike(entry));
    const y = normalized[normalized.length - 1];
    if (typeof y !== 'number' && y !== null) return undefined;
    return normalized;
  }
  const normalized = coerceNumericLike(value);
  if (typeof normalized === 'number' || normalized === null) return normalized;
  return undefined;
}

function normalizeScatterPointValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    if (value.length < 2) return undefined;
    const normalized = value.map((entry) => coerceNumericLike(entry));
    if (typeof normalized[0] !== 'number' || typeof normalized[1] !== 'number') return undefined;
    return normalized;
  }
  return undefined;
}

function normalizeSeriesDataPointByType(point: unknown, seriesType: string): unknown {
  const type = seriesType.trim().toLowerCase();
  if (type === 'line' || type === 'bar') {
    if (isPlainObject(point)) {
      const out: PlainObject = {};
      for (const [key, value] of Object.entries(point)) {
        if (key === '__proto__' || key === 'constructor' || key === 'prototype') continue;
        out[key] = coerceNumericLike(value);
      }

      let candidateValue: unknown;
      if (Object.prototype.hasOwnProperty.call(out, 'value')) {
        candidateValue = out.value;
      } else if (Object.prototype.hasOwnProperty.call(out, 'y')) {
        candidateValue = Object.prototype.hasOwnProperty.call(out, 'x')
          ? [out.x, out.y]
          : out.y;
      }
      if (candidateValue === undefined) return undefined;

      const normalizedValue = normalizeCartesianPointValue(candidateValue);
      if (normalizedValue === undefined) return undefined;
      out.value = normalizedValue;
      delete out.x;
      delete out.y;
      return out;
    }
    return normalizeCartesianPointValue(point);
  }

  if (type === 'scatter' || type === 'effectscatter') {
    if (isPlainObject(point)) {
      const out: PlainObject = {};
      for (const [key, value] of Object.entries(point)) {
        if (key === '__proto__' || key === 'constructor' || key === 'prototype') continue;
        out[key] = coerceNumericLike(value);
      }

      let candidateValue: unknown;
      if (Object.prototype.hasOwnProperty.call(out, 'value')) {
        candidateValue = out.value;
      } else if (Object.prototype.hasOwnProperty.call(out, 'x') && Object.prototype.hasOwnProperty.call(out, 'y')) {
        candidateValue = [out.x, out.y];
      }
      if (candidateValue === undefined) return undefined;

      const normalizedValue = normalizeScatterPointValue(candidateValue);
      if (normalizedValue === undefined) return undefined;
      out.value = normalizedValue;
      delete out.x;
      delete out.y;
      return out;
    }
    return normalizeScatterPointValue(point);
  }

  return point;
}

interface NormalizedSeriesDataResult {
  data: unknown[];
  droppedCount: number;
}

function normalizeSeriesDataByType(data: unknown[], seriesType: string): NormalizedSeriesDataResult {
  const normalized: unknown[] = [];
  let droppedCount = 0;
  for (const point of data) {
    const next = normalizeSeriesDataPointByType(point, seriesType);
    if (next === undefined) {
      droppedCount += 1;
      continue;
    }
    normalized.push(next);
  }
  return { data: normalized, droppedCount };
}

function sanitizeAnyValue(value: unknown): any {
  if (Array.isArray(value)) {
    return value
      .map((entry) => sanitizeAnyValue(entry))
      .filter((entry) => entry !== undefined);
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
      const sanitized = sanitizeSeriesDataPoint(value);
      if (sanitized !== undefined) out[key] = sanitized;
    }
    return out;
  }
  return sanitizeScalar(point);
}

function supportedSeriesForModule(module: EchartsModule): Set<string> {
  return SERIES_TYPES_BY_MODULE[module]
    || SERIES_TYPES_BY_MODULE.core
    || new Set(FALLBACK_SERIES_TYPES_BY_MODULE.core);
}

function supportedOptionRootsForModule(module: EchartsModule): Set<string> {
  return OPTION_ROOTS_BY_MODULE[module]
    || OPTION_ROOTS_BY_MODULE.core
    || new Set(FALLBACK_OPTION_ROOTS);
}

function allowedCallbackIDs(kind: string): Set<string> {
  const key = kind.trim().toLowerCase();
  return CALLBACK_IDS_BY_KIND[key]
    || new Set(FALLBACK_CALLBACKS_BY_KIND[key] || []);
}

function sanitizeSeries(rawSeries: unknown, module: EchartsModule, warnings: string[]): any[] | { error: string } {
  if (!Array.isArray(rawSeries)) {
    return { error: 'echarts_spec.options.series must be an array.' };
  }
  if (rawSeries.length === 0) {
    return { error: 'echarts_spec.options.series cannot be empty.' };
  }

  const allowedTypes = supportedSeriesForModule(module);
  const customRenderAllowed = allowedCallbackIDs('series.custom.render_item');
  const out: any[] = [];

  for (let i = 0; i < rawSeries.length; i += 1) {
    const entry = rawSeries[i];
    if (!isPlainObject(entry)) {
      return { error: `series[${i}] must be an object.` };
    }

    const type = String(entry.type || '').trim().toLowerCase();
    if (!type) {
      return { error: `series[${i}].type is required.` };
    }
    if (!allowedTypes.has(type)) {
      return { error: `series[${i}].type "${type}" is not supported for module "${module}".` };
    }

    const seriesOut: PlainObject = { type };
    let hasData = false;
    let renderItemID = '';

    for (const [key, value] of Object.entries(entry)) {
      if (key === 'type') continue;

      if (key === 'data') {
        if (!Array.isArray(value)) {
          return { error: `series[${i}].data must be an array.` };
        }
        const sanitizedData: unknown[] = [];
        let droppedUnsupported = 0;
        for (const point of value) {
          const sanitizedPoint = sanitizeSeriesDataPoint(point);
          if (sanitizedPoint === undefined) {
            droppedUnsupported += 1;
            continue;
          }
          sanitizedData.push(sanitizedPoint);
        }
        const normalizedData = normalizeSeriesDataByType(sanitizedData, type);
        const droppedCount = droppedUnsupported + normalizedData.droppedCount;
        if (normalizedData.data.length === 0) {
          return { error: `series[${i}].data has no valid points after normalization.` };
        }
        if (droppedCount > 0) {
          warnings.push(`Dropped ${droppedCount} invalid data point${droppedCount === 1 ? '' : 's'} from series[${i}].`);
        }
        seriesOut.data = normalizedData.data;
        hasData = true;
        continue;
      }

      if (key === 'renderItem') {
        return { error: `series[${i}].renderItem is not allowed; use render_item_id.` };
      }

      if (key === 'render_item_id') {
        const id = String(value || '').trim();
        if (!id) {
          return { error: `series[${i}].render_item_id must be a non-empty string.` };
        }
        renderItemID = id;
        continue;
      }

      const sanitized = sanitizeAnyValue(value);
      if (sanitized === undefined) {
        return { error: `series[${i}].${key} contains unsupported values.` };
      }
      seriesOut[key] = sanitized;
      if (key === 'nodes' || key === 'links') hasData = true;
    }

    if (type === 'custom') {
      if (!renderItemID) {
        return { error: `series[${i}].render_item_id is required for custom series.` };
      }
      if (!customRenderAllowed.has(renderItemID)) {
        return { error: `unsupported series[${i}].render_item_id "${renderItemID}".` };
      }
      const renderItem = getEchartsSeriesRenderItemByID(renderItemID);
      if (!renderItem) {
        return { error: `No renderer is registered for series[${i}].render_item_id "${renderItemID}".` };
      }
      seriesOut.render_item_id = renderItemID;
      seriesOut.renderItem = renderItem;
    } else {
      if (renderItemID) {
        return { error: `series[${i}].render_item_id is only supported for custom series.` };
      }
      if (!hasData) {
        return { error: `series[${i}].data is required.` };
      }
    }

    out.push(seriesOut);
  }

  return out;
}

function sanitizeTooltip(raw: unknown): PlainObject | { error: string } {
  if (!isPlainObject(raw)) {
    return { error: 'echarts_spec.options.tooltip must be an object.' };
  }

  const out: PlainObject = {};
  const trigger = String(raw.trigger || '').trim();
  if (trigger) out.trigger = trigger;
  const triggerOn = String(raw.triggerOn || '').trim();
  if (triggerOn) out.triggerOn = triggerOn;
  if (typeof raw.show === 'boolean') out.show = raw.show;
  if (typeof raw.confine === 'boolean') out.confine = raw.confine;
  else out.confine = true;
  const renderMode = String(raw.renderMode || '').trim();
  if (renderMode) out.renderMode = renderMode;

  if (Object.prototype.hasOwnProperty.call(raw, 'formatter')) {
    return { error: 'echarts_spec.options.tooltip.formatter is not allowed; use formatter_id.' };
  }

  const formatterID = String((raw as any).formatter_id || '').trim();
  if (formatterID) {
    const allowed = allowedCallbackIDs('tooltip.formatter');
    if (!allowed.has(formatterID)) {
      return { error: `unsupported tooltip.formatter_id "${formatterID}".` };
    }
    const formatter = getEchartsTooltipFormatterByID(formatterID);
    if (!formatter) {
      return { error: `No formatter is registered for tooltip.formatter_id "${formatterID}".` };
    }
    out.formatter_id = formatterID;
    out.formatter = formatter;
  }

  return out;
}

function normalizeModules(raw: unknown): string[] | { error: string } {
  if (raw === undefined || raw === null) return [];
  if (!Array.isArray(raw)) {
    return { error: 'echarts_spec.modules must be an array.' };
  }

  const out: string[] = [];
  const seen = new Set<string>();
  for (let i = 0; i < raw.length; i += 1) {
    const moduleName = String(raw[i] || '').trim().toLowerCase();
    if (!moduleName) {
      return { error: `echarts_spec.modules[${i}] must be a non-empty string.` };
    }
    if (!SUPPORTED_EXTENSION_MODULES.has(moduleName)) {
      return { error: `echarts_spec.modules[${i}] "${moduleName}" is not a supported extension module.` };
    }
    if (seen.has(moduleName)) continue;
    seen.add(moduleName);
    out.push(moduleName);
  }
  out.sort();
  return out;
}

function sanitizeDrilldownAsync(raw: unknown, module: EchartsModule): SanitizedEchartsSpec['extensions']['drilldown_async'] | { error: string } {
  if (!isPlainObject(raw)) {
    return { error: 'echarts_spec.extensions.drilldown_async must be an object.' };
  }

  const loaderID = String(raw.loader_id || '').trim();
  if (!loaderID) {
    return { error: 'echarts_spec.extensions.drilldown_async.loader_id is required.' };
  }
  const allowedLoaders = allowedCallbackIDs('drilldown.loader');
  if (allowedLoaders.size > 0 && !allowedLoaders.has(loaderID)) {
    return { error: `unsupported drilldown_async.loader_id "${loaderID}".` };
  }

  if (!isPlainObject(raw.series_by_id)) {
    return { error: 'echarts_spec.extensions.drilldown_async.series_by_id must be an object.' };
  }
  const entries = Object.entries(raw.series_by_id);
  if (entries.length === 0) {
    return { error: 'echarts_spec.extensions.drilldown_async.series_by_id cannot be empty.' };
  }
  if (entries.length > 200) {
    return { error: 'echarts_spec.extensions.drilldown_async.series_by_id exceeds max entries (200).' };
  }

  const normalizedSeriesByID: Record<string, Record<string, any>> = {};
  for (const [id, seriesSpec] of entries) {
    const seriesID = id.trim();
    if (!seriesID) {
      return { error: 'echarts_spec.extensions.drilldown_async.series_by_id contains an empty series id.' };
    }
    if (!isPlainObject(seriesSpec)) {
      return { error: `echarts_spec.extensions.drilldown_async.series_by_id["${seriesID}"] must be an object.` };
    }
    if (hasExecutablePayload(seriesSpec)) {
      return { error: `Executable payloads are not allowed in extensions.drilldown_async.series_by_id.${seriesID}.` };
    }
    const seriesResult = sanitizeSeries([seriesSpec], module, []);
    if ('error' in seriesResult) {
      return { error: `echarts_spec.extensions.drilldown_async.series_by_id["${seriesID}"]: ${seriesResult.error}` };
    }
    normalizedSeriesByID[seriesID] = seriesResult[0];
  }

  const parsedDepth = Number(raw.max_depth);
  const maxDepth = Number.isFinite(parsedDepth) ? Math.trunc(parsedDepth) : 4;
  if (maxDepth < 1 || maxDepth > 8) {
    return { error: 'echarts_spec.extensions.drilldown_async.max_depth must be between 1 and 8.' };
  }

  return {
    loader_id: loaderID,
    series_by_id: normalizedSeriesByID,
    max_depth: maxDepth,
  };
}

export function sanitizeEchartsPlotSpec(rawSpec: unknown): SanitizedEchartsSpec | { ok: false; error: string } {
  if (!isPlainObject(rawSpec)) {
    return { ok: false, error: 'echarts_spec must be an object.' };
  }

  if (hasExecutablePayload(rawSpec)) {
    return { ok: false, error: 'Executable payloads are not allowed in echarts specs.' };
  }

  const warnings: string[] = [];
  const schemaVersion = String(rawSpec.schema_version || '').trim();
  if (schemaVersion !== 'echarts-plot-spec.v1') {
    return { ok: false, error: `Unsupported echarts schema_version: ${schemaVersion || '(missing)'}` };
  }

  const requestedModule = String(rawSpec.module || '').trim().toLowerCase();
  if (!requestedModule) {
    return { ok: false, error: 'echarts_spec.module is required.' };
  }
  if (!SUPPORTED_MODULES.has(requestedModule)) {
    return { ok: false, error: `Unsupported echarts module: ${requestedModule}` };
  }
  const module = requestedModule as EchartsModule;

  if (!isPlainObject(rawSpec.options)) {
    return { ok: false, error: 'echarts_spec.options must be an object.' };
  }

  const modulesResult = normalizeModules(rawSpec.modules);
  if ('error' in modulesResult) {
    return { ok: false, error: modulesResult.error };
  }

  const optionRootSet = supportedOptionRootsForModule(module);
  const optionsOut: PlainObject = {};
  for (const [key, value] of Object.entries(rawSpec.options)) {
    if (!optionRootSet.has(key)) {
      return { ok: false, error: `unsupported options key "${key}" for module "${module}".` };
    }

    if (key === 'series') {
      const seriesResult = sanitizeSeries(value, module, warnings);
      if ('error' in seriesResult) {
        return { ok: false, error: seriesResult.error };
      }
      optionsOut.series = seriesResult;
      continue;
    }

    if (key === 'tooltip') {
      const tooltipResult = sanitizeTooltip(value);
      if ('error' in tooltipResult) {
        return { ok: false, error: tooltipResult.error };
      }
      optionsOut.tooltip = tooltipResult;
      continue;
    }

    const sanitized = sanitizeAnyValue(value);
    if (sanitized === undefined) {
      return { ok: false, error: `options.${key} contains unsupported values.` };
    }
    optionsOut[key] = sanitized;
  }

  if (!Array.isArray(optionsOut.series) || optionsOut.series.length === 0) {
    return { ok: false, error: 'echarts_spec.options.series is required and must contain at least one valid series.' };
  }

  const extensionsOut: SanitizedEchartsSpec['extensions'] = {};
  if (rawSpec.extensions !== undefined) {
    if (!isPlainObject(rawSpec.extensions)) {
      return { ok: false, error: 'echarts_spec.extensions must be an object.' };
    }
    for (const key of Object.keys(rawSpec.extensions)) {
      if (key !== 'drilldown_async') {
        return { ok: false, error: `echarts_spec.extensions.${key} is not supported.` };
      }
    }
    if (Object.prototype.hasOwnProperty.call(rawSpec.extensions, 'drilldown_async')) {
      const drilldownAsync = sanitizeDrilldownAsync((rawSpec.extensions as any).drilldown_async, module);
      if ('error' in drilldownAsync) {
        return { ok: false, error: drilldownAsync.error };
      }
      extensionsOut.drilldown_async = drilldownAsync;
    }
  }

  return {
    ok: true,
    schemaVersion: 'echarts-plot-spec.v1',
    module,
    modules: modulesResult,
    options: optionsOut as EChartsOption,
    warnings,
    ...(Object.keys(extensionsOut).length > 0 ? { extensions: extensionsOut } : {}),
  };
}
