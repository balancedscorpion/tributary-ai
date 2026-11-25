import React from 'react';
import ReactECharts from 'echarts-for-react';
import type { ECharts, EChartsOption } from 'echarts';
import { AlertTriangle, ChevronLeft } from 'lucide-react';
import { sanitizeEchartsPlotSpec, type SanitizedEchartsSpec } from './echartsSpecSanitizer';
import {
  applyEchartsDrilldownClick,
  canStepBackDrilldown,
  createEchartsDrilldownState,
  resolveActiveSeries,
  stepBackEchartsDrilldown,
} from './echartsDrilldownController';

interface EchartsVisualizationProps {
  plotSpec: unknown;
  className?: string;
  title?: string;
}

const ECHARTS_DARK_BASE_OPTIONS: EChartsOption = {
  backgroundColor: '#0b1220',
  textStyle: {
    color: '#e2e8f0',
  },
  title: {
    textStyle: {
      color: '#f8fafc',
      fontWeight: 700,
    },
    subtextStyle: {
      color: '#cbd5e1',
    },
  },
  legend: {
    textStyle: {
      color: '#cbd5e1',
    },
  },
  tooltip: {
    confine: true,
    backgroundColor: 'rgba(15, 23, 42, 0.95)',
    borderColor: 'rgba(148, 163, 184, 0.35)',
    textStyle: {
      color: '#f8fafc',
    },
  },
  xAxis: {
    axisLine: { lineStyle: { color: 'rgba(148, 163, 184, 0.35)' } },
    axisTick: { lineStyle: { color: 'rgba(148, 163, 184, 0.35)' } },
    axisLabel: { color: '#cbd5e1' },
    nameTextStyle: { color: '#e2e8f0' },
    splitLine: { lineStyle: { color: 'rgba(148, 163, 184, 0.2)' } },
  },
  yAxis: {
    axisLine: { lineStyle: { color: 'rgba(148, 163, 184, 0.35)' } },
    axisTick: { lineStyle: { color: 'rgba(148, 163, 184, 0.35)' } },
    axisLabel: { color: '#cbd5e1' },
    nameTextStyle: { color: '#e2e8f0' },
    splitLine: { lineStyle: { color: 'rgba(148, 163, 184, 0.2)' } },
  },
  grid: {
    containLabel: true,
    right: 40,
    bottom: 8,
  },
};

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

function deepMergeEchartsOptions(base: EChartsOption, model: EChartsOption): EChartsOption {
  const result: Record<string, unknown> = {};
  const allKeys = new Set([...Object.keys(base), ...Object.keys(model)]);
  for (const key of allKeys) {
    const bVal = (base as Record<string, unknown>)[key];
    const mVal = (model as Record<string, unknown>)[key];
    if (mVal === undefined) {
      result[key] = bVal;
    } else if (bVal === undefined) {
      result[key] = mVal;
    } else if (isPlainObject(bVal) && Array.isArray(mVal) && (key === 'xAxis' || key === 'yAxis')) {
      // Dual-axis: model provides array, base provides single axis defaults — apply defaults to each element
      result[key] = mVal.map((item: unknown) =>
        isPlainObject(item) ? deepMergeEchartsOptions(bVal as EChartsOption, item as EChartsOption) : item,
      );
    } else if (isPlainObject(bVal) && isPlainObject(mVal)) {
      result[key] = deepMergeEchartsOptions(bVal as EChartsOption, mVal as EChartsOption);
    } else {
      result[key] = mVal;
    }
  }
  return result as EChartsOption;
}

type AxisConfig = Record<string, unknown>;

function toAxisConfigList(rawAxis: unknown): AxisConfig[] {
  if (Array.isArray(rawAxis)) {
    return rawAxis.map((entry) => (isPlainObject(entry) ? entry as AxisConfig : {}));
  }
  if (isPlainObject(rawAxis)) {
    return [rawAxis as AxisConfig];
  }
  return [];
}

function normalizeSeriesList(rawSeries: unknown): Array<Record<string, any>> {
  if (Array.isArray(rawSeries)) {
    return rawSeries
      .filter((entry) => isPlainObject(entry))
      .map((entry) => ({ ...(entry as Record<string, any>) }));
  }
  if (isPlainObject(rawSeries)) {
    return [{ ...(rawSeries as Record<string, any>) }];
  }
  return [];
}

function readExplicitAxisBool(axis: AxisConfig | undefined, key: string): boolean | undefined {
  if (!axis) return undefined;
  const value = axis[key];
  return typeof value === 'boolean' ? value : undefined;
}

function readExplicitNestedAxisBool(axis: AxisConfig | undefined, key: string, nestedKey: string): boolean | undefined {
  if (!axis) return undefined;
  const nested = axis[key];
  if (!isPlainObject(nested)) return undefined;
  const value = (nested as AxisConfig)[nestedKey];
  return typeof value === 'boolean' ? value : undefined;
}

function normalizeMultiAxisGridlines(mergedOption: EChartsOption, specOption: EChartsOption): EChartsOption {
  const mergedYAxis = toAxisConfigList((mergedOption as AxisConfig).yAxis);
  if (mergedYAxis.length <= 1) {
    return mergedOption;
  }

  const specYAxis = toAxisConfigList((specOption as AxisConfig).yAxis);
  const normalizedYAxis = mergedYAxis.map((axis, index) => {
    const nextAxis: AxisConfig = { ...axis };
    const specAxis = specYAxis[index];

    if (readExplicitAxisBool(specAxis, 'alignTicks') === undefined) {
      nextAxis.alignTicks = true;
    }

    if (index > 0) {
      if (readExplicitNestedAxisBool(specAxis, 'splitLine', 'show') === undefined) {
        const splitLine = isPlainObject(nextAxis.splitLine) ? { ...(nextAxis.splitLine as AxisConfig) } : {};
        splitLine.show = false;
        nextAxis.splitLine = splitLine;
      }
      if (readExplicitNestedAxisBool(specAxis, 'minorSplitLine', 'show') === undefined) {
        const minorSplitLine = isPlainObject(nextAxis.minorSplitLine) ? { ...(nextAxis.minorSplitLine as AxisConfig) } : {};
        minorSplitLine.show = false;
        nextAxis.minorSplitLine = minorSplitLine;
      }
    }

    return nextAxis;
  });

  (mergedOption as AxisConfig).yAxis = normalizedYAxis;
  return mergedOption;
}

function parseDimensionValue(value: unknown, total: number, fallback: number): number {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return Math.max(0, Math.round(value));
  }
  if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed) return fallback;
    if (trimmed.endsWith('%')) {
      const ratio = Number(trimmed.slice(0, -1));
      if (Number.isFinite(ratio)) {
        return Math.max(0, Math.round((ratio / 100) * Math.max(0, total)));
      }
    }
    const numeric = Number(trimmed);
    if (Number.isFinite(numeric)) {
      return Math.max(0, Math.round(numeric));
    }
  }
  return fallback;
}

function hasVisibleTitleText(option: EChartsOption): boolean {
  const title = (option as AxisConfig).title;
  const hasText = (entry: unknown): boolean => {
    if (!isPlainObject(entry)) return false;
    return String((entry as AxisConfig).text || '').trim().length > 0;
  };
  if (Array.isArray(title)) {
    return title.some((entry) => hasText(entry));
  }
  return hasText(title);
}

function normalizeTitleEntryLayout(
  rawTitleEntry: unknown,
  containerSize: { width: number; height: number },
): AxisConfig | null {
  if (!isPlainObject(rawTitleEntry)) return null;
  const titleEntry = { ...(rawTitleEntry as AxisConfig) };
  const text = String(titleEntry.text || '').trim();
  if (!text || titleEntry.show === false) {
    return titleEntry;
  }

  const width = Math.max(320, containerSize.width || 0);
  const hasLeft = isPositionExplicit(titleEntry.left);
  const hasRight = isPositionExplicit(titleEntry.right);
  const hasX = isPositionExplicit(titleEntry.x);
  if (!hasLeft && !hasRight && !hasX) {
    titleEntry.left = 12;
    titleEntry.right = 12;
  }
  if (!isPositionExplicit(titleEntry.top)) {
    titleEntry.top = 8;
  }

  const leftPx = parseDimensionValue(titleEntry.left, width, 12);
  const rightPx = parseDimensionValue(titleEntry.right, width, 12);
  const availableWidth = Math.max(120, width - leftPx - rightPx);

  const textStyle = isPlainObject(titleEntry.textStyle)
    ? { ...(titleEntry.textStyle as AxisConfig) }
    : {};

  if (textStyle.width === undefined) {
    textStyle.width = availableWidth;
  }
  if (textStyle.overflow === undefined) {
    textStyle.overflow = 'truncate';
  }
  if (textStyle.ellipsis === undefined) {
    textStyle.ellipsis = '...';
  }
  if (textStyle.align === undefined && !hasLeft && !hasRight && !hasX) {
    textStyle.align = 'left';
  }
  titleEntry.textStyle = textStyle;

  return titleEntry;
}

function normalizeTitleLayout(option: EChartsOption, containerSize: { width: number; height: number }): EChartsOption {
  const root = { ...(option as AxisConfig) };
  const rawTitle = root.title;

  if (Array.isArray(rawTitle)) {
    const normalized = rawTitle.map((entry) => normalizeTitleEntryLayout(entry, containerSize));
    root.title = normalized.map((entry, index) => entry ?? rawTitle[index]);
    return root as EChartsOption;
  }

  const normalizedTitle = normalizeTitleEntryLayout(rawTitle, containerSize);
  if (normalizedTitle) {
    root.title = normalizedTitle;
  }
  return root as EChartsOption;
}

function extractLegendNames(option: EChartsOption): string[] {
  const names = new Set<string>();
  const legend = (option as AxisConfig).legend;
  const collectFromLegendData = (entry: unknown): void => {
    if (!isPlainObject(entry)) return;
    const data = (entry as AxisConfig).data;
    if (!Array.isArray(data)) return;
    for (const datum of data) {
      if (typeof datum === 'string') {
        const text = datum.trim();
        if (text) names.add(text);
        continue;
      }
      if (isPlainObject(datum)) {
        const text = String((datum as AxisConfig).name || '').trim();
        if (text) names.add(text);
      }
    }
  };

  if (Array.isArray(legend)) {
    for (const entry of legend) collectFromLegendData(entry);
  } else {
    collectFromLegendData(legend);
  }

  const series = Array.isArray(option.series) ? option.series : [];
  for (const entry of series) {
    if (!isPlainObject(entry)) continue;
    const text = String((entry as AxisConfig).name || '').trim();
    if (text) names.add(text);
  }
  return [...names];
}

function isPositionExplicit(value: unknown): boolean {
  if (value === undefined || value === null) return false;
  return String(value).trim().toLowerCase() !== 'auto';
}

function normalizeLegendAndGridLayout(
  option: EChartsOption,
  containerSize: { width: number; height: number },
): EChartsOption {
  const root = { ...(option as AxisConfig) };
  const rawGrid = root.grid;
  if (!isPlainObject(rawGrid)) {
    return option;
  }

  const grid = { ...(rawGrid as AxisConfig) };
  const rawLegend = root.legend;
  let legendIndex = -1;
  let legend: AxisConfig | null = null;

  if (Array.isArray(rawLegend)) {
    for (let i = 0; i < rawLegend.length; i += 1) {
      const entry = rawLegend[i];
      if (!isPlainObject(entry)) continue;
      if ((entry as AxisConfig).show === false) continue;
      legendIndex = i;
      legend = { ...(entry as AxisConfig) };
      break;
    }
  } else if (isPlainObject(rawLegend)) {
    legend = { ...(rawLegend as AxisConfig) };
  }
  if (!legend || legend.show === false) {
    return option;
  }

  const width = Math.max(320, containerSize.width || 0);
  const height = Math.max(220, containerSize.height || 0);
  const legendNames = extractLegendNames(option);
  const itemCount = Math.max(1, legendNames.length);
  const fontSize = parseDimensionValue(
    isPlainObject(legend.textStyle) ? (legend.textStyle as AxisConfig).fontSize : undefined,
    width,
    12,
  );
  const itemWidth = parseDimensionValue(legend.itemWidth, width, 14);
  const itemHeight = parseDimensionValue(legend.itemHeight, height, 14);
  const itemGap = parseDimensionValue(legend.itemGap, width, 12);
  const avgChars = legendNames.length > 0
    ? legendNames.reduce((sum, name) => sum + Math.min(28, name.length), 0) / legendNames.length
    : 12;
  const approxItemWidth = Math.min(
    260,
    Math.max(90, Math.round(itemWidth + itemGap + (avgChars * fontSize * 0.58) + 24)),
  );
  const availableWidth = Math.max(220, width - 32);
  const itemsPerRow = Math.max(1, Math.floor(availableWidth / approxItemWidth));
  const rowsNeeded = Math.max(1, Math.ceil(itemCount / itemsPerRow));
  const legendRows = Math.min(rowsNeeded, 2);
  const legendHeightPx = (legendRows * (itemHeight + 8)) + 8;
  const legendWidthPx = Math.min(240, Math.max(140, Math.round(width * 0.24)));

  const xAxes = toAxisConfigList(root.xAxis);
  const hasXAxisName = xAxes.some((axis) => String(axis.name || '').trim().length > 0);
  const xAxisReserve = hasXAxisName ? 52 : 34;
  const titleReserve = hasVisibleTitleText(option) ? 42 : 10;

  const orient = String(legend.orient || '').trim().toLowerCase();
  const isVertical = orient === 'vertical';
  const hasTop = isPositionExplicit(legend.top);
  const hasBottom = isPositionExplicit(legend.bottom);
  const hasLeft = isPositionExplicit(legend.left);
  const hasRight = isPositionExplicit(legend.right);

  if (!isVertical) {
    legend.orient = 'horizontal';
    if (!isPositionExplicit(legend.type)) {
      legend.type = rowsNeeded > 1 ? 'scroll' : 'plain';
    }
    legend.left = hasLeft ? legend.left : 'center';

    if (!hasTop) {
      legend.bottom = hasBottom ? legend.bottom : 8;
      const legendBottomPx = parseDimensionValue(legend.bottom, height, 8);
      const requiredBottom = legendBottomPx + legendHeightPx + xAxisReserve;
      const currentBottom = parseDimensionValue(grid.bottom, height, 8);
      grid.bottom = Math.max(currentBottom, requiredBottom);
    } else {
      const legendTopPx = parseDimensionValue(legend.top, height, 8);
      const requiredTop = legendTopPx + legendHeightPx + titleReserve;
      const currentTop = parseDimensionValue(grid.top, height, titleReserve);
      grid.top = Math.max(currentTop, requiredTop);
    }
  } else {
    if (!isPositionExplicit(legend.type)) {
      legend.type = 'scroll';
    }

    if (hasLeft && !hasRight) {
      legend.left = legend.left ?? 8;
      const leftPx = parseDimensionValue(legend.left, width, 8);
      const requiredLeft = leftPx + legendWidthPx + 10;
      const currentLeft = parseDimensionValue(grid.left, width, 10);
      grid.left = Math.max(currentLeft, requiredLeft);
    } else {
      legend.right = legend.right ?? 8;
      const rightPx = parseDimensionValue(legend.right, width, 8);
      const requiredRight = rightPx + legendWidthPx + 10;
      const currentRight = parseDimensionValue(grid.right, width, 10);
      grid.right = Math.max(currentRight, requiredRight);
    }

    if (!hasTop && !hasBottom) {
      legend.top = hasVisibleTitleText(option) ? 48 : 10;
    }
  }

  grid.containLabel = true;
  root.grid = grid;
  if (Array.isArray(rawLegend) && legendIndex >= 0) {
    const updatedLegend = [...rawLegend];
    updatedLegend[legendIndex] = legend;
    root.legend = updatedLegend;
  } else {
    root.legend = legend;
  }
  return root as EChartsOption;
}

function stableSerialize(value: unknown): string {
  const seen = new WeakSet<object>();
  const normalize = (input: unknown): unknown => {
    if (Array.isArray(input)) return input.map(normalize);
    if (!input || typeof input !== 'object') return input;
    const typed = input as Record<string, unknown>;
    if (seen.has(typed)) return '[circular]';
    seen.add(typed);
    const sortedEntries = Object.keys(typed)
      .sort()
      .map((key) => [key, normalize(typed[key])] as const);
    return Object.fromEntries(sortedEntries);
  };
  try {
    return JSON.stringify(normalize(value));
  } catch {
    return '';
  }
}

function readTitleFromOption(option: EChartsOption): string {
  const raw = (option.title as any)?.text;
  const text = String(raw || '').trim();
  if (!text) return '';
  const lowered = text.toLowerCase();
  if (lowered === 'chart title' || lowered === 'title' || lowered === 'untitled chart') return '';
  return text;
}

function resolveChartTitle(option: EChartsOption, preferredTitle?: string): string {
  const fromOption = readTitleFromOption(option);
  if (fromOption) return fromOption;
  const preferred = String(preferredTitle || '').trim();
  if (preferred) return preferred;

  const series = Array.isArray(option.series) ? option.series : [];
  for (const entry of series) {
    const name = String((entry as any)?.name || '').trim();
    if (name) return `${name} Overview`;
  }
  return 'Data Chart';
}

const EchartsVisualizationComponent: React.FC<EchartsVisualizationProps> = ({ plotSpec, className, title }) => {
  const incomingSanitized = React.useMemo(() => sanitizeEchartsPlotSpec(plotSpec), [plotSpec]);
  const [stableSanitized, setStableSanitized] = React.useState<SanitizedEchartsSpec | null>(
    (incomingSanitized as SanitizedEchartsSpec).ok ? (incomingSanitized as SanitizedEchartsSpec) : null,
  );
  const effectiveSanitized = (incomingSanitized as SanitizedEchartsSpec).ok
    ? (incomingSanitized as SanitizedEchartsSpec)
    : stableSanitized;

  const hostRef = React.useRef<HTMLDivElement | null>(null);
  const echartsRef = React.useRef<ReactECharts | null>(null);
  const chartInstanceRef = React.useRef<ECharts | null>(null);
  const [containerSize, setContainerSize] = React.useState({ width: 0, height: 0 });
  const [drilldownState, setDrilldownState] = React.useState(createEchartsDrilldownState);
  const [drilldownWarnings, setDrilldownWarnings] = React.useState<string[]>([]);

  React.useEffect(() => {
    if (!(incomingSanitized as SanitizedEchartsSpec).ok) return;
    const next = incomingSanitized as SanitizedEchartsSpec;
    setStableSanitized((current) => {
      if (!current) return next;
      const currentSignature = stableSerialize({
        module: current.module,
        modules: current.modules,
        options: current.options,
        extensions: current.extensions,
      });
      const nextSignature = stableSerialize({
        module: next.module,
        modules: next.modules,
        options: next.options,
        extensions: next.extensions,
      });
      if (currentSignature === nextSignature) return current;
      return next;
    });
  }, [incomingSanitized]);

  React.useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const update = () => {
      const rect = el.getBoundingClientRect();
      const next = {
        width: Math.max(1, Math.round(rect.width)),
        height: Math.max(1, Math.round(rect.height)),
      };
      setContainerSize((prev) => (prev.width === next.width && prev.height === next.height ? prev : next));
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  React.useEffect(() => {
    if (!chartInstanceRef.current) return;
    chartInstanceRef.current.resize();
  }, [containerSize.height, containerSize.width]);

  if (!effectiveSanitized?.ok) {
    const error = incomingSanitized && !(incomingSanitized as SanitizedEchartsSpec).ok
      ? (incomingSanitized as { ok: false; error: string }).error
      : 'Invalid ECharts spec.';
    return (
      <div className={`h-full w-full rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 ${className || ''}`}>
        <div className="flex items-start gap-2 text-amber-200">
          <AlertTriangle size={16} className="mt-0.5 shrink-0" />
          <div className="text-sm">
            <div className="font-medium">Unable to render ECharts spec.</div>
            <div className="mt-1 text-xs text-amber-100/90">{error}</div>
          </div>
        </div>
      </div>
    );
  }

  const pushDrilldownWarning = React.useCallback((message: string) => {
    const text = String(message || '').trim();
    if (!text) return;
    setDrilldownWarnings((current) => (current.includes(text) ? current : [...current, text]));
  }, []);

  const baseSeries = React.useMemo(
    () => normalizeSeriesList((effectiveSanitized.options as Record<string, unknown>).series),
    [effectiveSanitized.options],
  );
  const drilldownConfig = effectiveSanitized.extensions?.drilldown_async;
  const drilldownResetSignature = React.useMemo(() => stableSerialize({
    baseSeries,
    drilldownConfig,
  }), [baseSeries, drilldownConfig]);

  React.useEffect(() => {
    setDrilldownState(createEchartsDrilldownState());
    setDrilldownWarnings([]);
  }, [drilldownResetSignature]);

  React.useEffect(() => {
    const chart = chartInstanceRef.current;
    if (!chart || !drilldownConfig) return;

    const onClick = (params: any) => {
      setDrilldownState((current) => {
        const step = applyEchartsDrilldownClick(current, params, drilldownConfig, baseSeries);
        if (step.warning) {
          pushDrilldownWarning(step.warning);
        }
        return step.state;
      });
    };

    chart.on('click', onClick);
    return () => {
      chart.off('click', onClick);
    };
  }, [baseSeries, drilldownConfig, pushDrilldownWarning]);

  const onBackDrilldown = React.useCallback(() => {
    setDrilldownState((current) => stepBackEchartsDrilldown(current).state);
  }, []);

  const resolvedTitle = resolveChartTitle(effectiveSanitized.options, title);
  const mergedOption = deepMergeEchartsOptions(ECHARTS_DARK_BASE_OPTIONS, {
    ...effectiveSanitized.options,
    title: {
      ...((effectiveSanitized.options as any).title || {}),
      text: resolvedTitle,
    },
  });
  const optionWithGridlines = normalizeMultiAxisGridlines(mergedOption, effectiveSanitized.options);
  const optionWithTitleLayout = normalizeTitleLayout(optionWithGridlines, containerSize);
  const optionWithLayout: EChartsOption = normalizeLegendAndGridLayout(optionWithTitleLayout, containerSize);

  const activeSeries = resolveActiveSeries(drilldownState, baseSeries);
  const option: EChartsOption = {
    ...(optionWithLayout as Record<string, unknown>),
    ...(activeSeries.length > 0 ? { series: activeSeries } : {}),
  };
  const combinedWarnings = [...effectiveSanitized.warnings, ...drilldownWarnings];
  const hasBackNav = canStepBackDrilldown(drilldownState);

  return (
    <div ref={hostRef} data-testid="echarts-visualization" className={`relative h-full w-full overflow-hidden ${className || ''}`}>
      {hasBackNav && (
        <button
          type="button"
          onClick={onBackDrilldown}
          className="absolute left-3 top-3 z-20 inline-flex items-center gap-1 rounded-md border border-slate-500/40 bg-slate-950/85 px-2 py-1 text-[11px] text-slate-100 transition hover:border-slate-300/60 hover:bg-slate-900/95"
        >
          <ChevronLeft size={12} />
          Back
        </button>
      )}

      <ReactECharts
        ref={echartsRef}
        option={option}
        notMerge={false}
        lazyUpdate={true}
        style={{ width: '100%', height: '100%' }}
        opts={{ renderer: 'canvas' }}
        onChartReady={(instance) => {
          chartInstanceRef.current = instance;
          instance.resize();
        }}
      />

      {combinedWarnings.length > 0 && (
        <div className="pointer-events-none absolute inset-x-3 bottom-3 z-10 rounded-md border border-amber-500/30 bg-slate-950/75 px-3 py-2 text-[11px] text-amber-100">
          {combinedWarnings.join(' ')}
        </div>
      )}
    </div>
  );
};

const EchartsVisualization = React.memo(EchartsVisualizationComponent);
EchartsVisualization.displayName = 'EchartsVisualization';

export default EchartsVisualization;
