import React from 'react';
import ReactECharts from 'echarts-for-react';
import type { EChartsOption } from 'echarts';
import { sanitizeEchartsPlotSpec, type SanitizedEchartsSpec } from '@/components/charting/echartsSpecSanitizer';
import Highcharts from 'highcharts';
import HighchartsReact from 'highcharts-react-official';
import { sanitizeHighchartsPlotSpec, type SanitizedHighchartsSpec } from '@/components/charting/highchartsSpecSanitizer';
import {
  loadHighchartsModules,
  resolveHighchartsConstructor,
  type HighchartsConstructorType,
} from '@/components/charting/HighchartsModuleLoader';

interface InlineChartPreviewProps {
  plotSpec: unknown;
  height?: number;
}

// Intentional palette — designed against hsl(0 0% 5.5%) dark surface.
// Primary pair: data-blue + accent-orange. Extended with greens, purples, teals.
const DESIGN_PALETTE = [
  '#4dacf0', // data-blue  — primary series
  '#e8863a', // accent-orange — secondary series / contrast
  '#5bd8a0', // chart-green — tertiary
  '#a78bfa', // soft purple
  '#38bdf8', // sky blue
  '#f472b6', // rose
  '#2dd4bf', // teal
  '#fbbf24', // warm yellow
  '#818cf8', // indigo
  '#34d399', // emerald
];

// ---------------------------------------------------------------------------
// Preview transformation — turns a full-detail ECharts config into a compact
// visual thumbnail by stripping all chrome (axes, labels, legends, tooltips)
// and applying chart-type-specific adjustments.
// ---------------------------------------------------------------------------

function hideAxis(axis: unknown): unknown {
  if (Array.isArray(axis)) return axis.map((a) => ({ ...a, show: false }));
  if (axis && typeof axis === 'object') return { ...axis, show: false };
  return { show: false };
}

function toPreviewSeries(series: any[]): any[] {
  return series.map((s) => {
    const base = {
      ...s,
      label: { show: false },
      emphasis: { ...(s.emphasis || {}), label: { show: false } },
    };

    // Pie / donut — center it, use a donut shape, kill labels
    if (s.type === 'pie') {
      return {
        ...base,
        radius: ['35%', '65%'],
        center: ['50%', '50%'],
        labelLine: { show: false },
        itemStyle: {
          ...(s.itemStyle || {}),
          borderColor: 'hsl(0 0% 5.5%)',
          borderWidth: 2,
        },
      };
    }

    // Funnel / sunburst / treemap — constrain and strip labels
    if (s.type === 'funnel' || s.type === 'sunburst' || s.type === 'treemap') {
      return {
        ...base,
        label: { show: false },
        ...(s.type === 'treemap' ? { breadcrumb: { show: false } } : {}),
      };
    }

    // Bar charts — fill the space, substantial bar widths
    if (s.type === 'bar') {
      return {
        ...base,
        barMinWidth: 6,
        barMaxWidth: 40,
        barGap: '20%',
        barCategoryGap: '30%',
      };
    }

    // Line / area — make lines slightly thicker for visibility at small size
    if (s.type === 'line') {
      return {
        ...base,
        lineStyle: { ...(s.lineStyle || {}), width: Math.max(s.lineStyle?.width || 2, 2) },
        showSymbol: false, // dots clutter at small size
        areaStyle: s.areaStyle ? { ...s.areaStyle, opacity: s.areaStyle.opacity ?? 0.15 } : undefined,
      };
    }

    // Scatter — ensure dots are visible
    if (s.type === 'scatter' || s.type === 'effectScatter') {
      return {
        ...base,
        symbolSize: Math.max(s.symbolSize || 4, 4),
      };
    }

    return base;
  });
}

function toPreviewOptions(fullOptions: Record<string, any>): EChartsOption {
  const series = Array.isArray(fullOptions.series) ? fullOptions.series : [];
  const hasPie = series.some((s: any) => s.type === 'pie' || s.type === 'sunburst' || s.type === 'treemap');

  // For pie/donut/sunburst/treemap, we don't need axes or grid at all
  if (hasPie) {
    return {
      ...fullOptions,
      backgroundColor: 'hsl(0 0% 5.5%)',
      color: fullOptions.color || DESIGN_PALETTE,
      title: { show: false },
      legend: { show: false },
      tooltip: { show: false },
      toolbox: { show: false },
      xAxis: { show: false },
      yAxis: { show: false },
      grid: { top: 8, bottom: 8, left: 8, right: 8 },
      series: toPreviewSeries(series),
      animation: true,
      animationDuration: 400,
    };
  }

  // For all cartesian charts (bar, line, scatter, etc.) — strip axis chrome,
  // push the drawing area to the edges so bars/lines fill the thumbnail.
  return {
    ...fullOptions,
    backgroundColor: 'hsl(0 0% 5.5%)',
    color: fullOptions.color || DESIGN_PALETTE,
    title: { show: false },
    legend: { show: false },
    tooltip: { show: false },
    toolbox: { show: false },
    xAxis: hideAxis(fullOptions.xAxis),
    yAxis: hideAxis(fullOptions.yAxis),
    grid: { top: 16, bottom: 16, left: '8%', right: '8%', containLabel: false },
    series: toPreviewSeries(series),
    animation: true,
    animationDuration: 400,
  };
}

function hideHighchartsAxis(axis: unknown): unknown {
  const hideSingleAxis = (raw: any): Record<string, any> => ({
    ...(raw && typeof raw === 'object' ? raw : {}),
    visible: false,
    labels: { ...((raw && typeof raw === 'object' ? raw.labels : {}) || {}), enabled: false },
    title: { ...((raw && typeof raw === 'object' ? raw.title : {}) || {}), text: undefined },
    gridLineWidth: 0,
    lineWidth: 0,
    tickLength: 0,
    minorTickLength: 0,
    startOnTick: false,
    endOnTick: false,
  });

  if (Array.isArray(axis)) return axis.map((entry) => hideSingleAxis(entry));
  return hideSingleAxis(axis);
}

function toHighchartsPreviewSeries(series: any[]): any[] {
  return series.map((entry) => {
    const type = typeof entry?.type === 'string' ? entry.type.toLowerCase() : '';
    const base = {
      ...entry,
      animation: false,
      enableMouseTracking: false,
      dataLabels: { ...(entry?.dataLabels || {}), enabled: false },
      states: {
        ...(entry?.states || {}),
        inactive: { ...((entry?.states && entry.states.inactive) || {}), opacity: 1 },
      },
    } as Record<string, any>;

    if (type === 'pie') {
      return {
        ...base,
        size: '84%',
        innerSize: base.innerSize || '52%',
        center: ['50%', '50%'],
        borderWidth: 0,
      };
    }

    if (type === 'line' || type === 'spline' || type === 'area' || type === 'areaspline') {
      return {
        ...base,
        lineWidth: Math.max(Number(base.lineWidth || 2), 2),
        marker: { ...(base.marker || {}), enabled: false },
      };
    }

    if (type === 'column' || type === 'bar') {
      return {
        ...base,
        borderWidth: 0,
        groupPadding: 0.08,
        pointPadding: 0.06,
      };
    }

    if (type === 'scatter' || type === 'bubble') {
      return {
        ...base,
        marker: {
          ...(base.marker || {}),
          enabled: true,
          radius: Math.max(Number(base.marker?.radius || 3), 3),
        },
      };
    }

    if (type === 'funnel' || type === 'sunburst' || type === 'treemap') {
      return {
        ...base,
        showInLegend: false,
      };
    }

    return base;
  });
}

function toHighchartsPreviewOptions(fullOptions: Highcharts.Options): Highcharts.Options {
  const series = Array.isArray(fullOptions.series) ? fullOptions.series : [];
  const previewSeries = toHighchartsPreviewSeries(series as any[]);
  const plotOptions = Highcharts.merge({}, fullOptions.plotOptions || {}, {
    series: {
      animation: false,
      enableMouseTracking: false,
      dataLabels: { enabled: false },
      states: { inactive: { opacity: 1 } },
    },
    pie: {
      dataLabels: { enabled: false },
      size: '84%',
      innerSize: '52%',
      center: ['50%', '50%'],
    },
    funnel: {
      dataLabels: { enabled: false },
    },
    treemap: {
      dataLabels: { enabled: false },
    },
    sunburst: {
      dataLabels: { enabled: false },
    },
  }) as Highcharts.PlotOptions;

  return {
    ...fullOptions,
    chart: {
      ...(fullOptions.chart || {}),
      backgroundColor: 'hsl(0 0% 5.5%)',
      spacing: [8, 8, 8, 8],
      animation: false,
    },
    title: { text: undefined },
    subtitle: { text: undefined },
    caption: { text: undefined },
    legend: { ...(fullOptions.legend || {}), enabled: false },
    tooltip: { ...(fullOptions.tooltip || {}), enabled: false },
    credits: { ...(fullOptions.credits || {}), enabled: false },
    xAxis: hideHighchartsAxis(fullOptions.xAxis),
    yAxis: hideHighchartsAxis(fullOptions.yAxis),
    plotOptions,
    series: previewSeries as Highcharts.SeriesOptionsType[],
  };
}

// ---------------------------------------------------------------------------

const InlineChartPreviewInner: React.FC<InlineChartPreviewProps> = ({ plotSpec, height = 180 }) => {
  const echartsOption = React.useMemo(() => {
    const sanitized = sanitizeEchartsPlotSpec(plotSpec);
    if (!sanitized || !(sanitized as SanitizedEchartsSpec).ok) return null;

    const spec = sanitized as SanitizedEchartsSpec;
    return toPreviewOptions(spec.options as Record<string, any>);
  }, [plotSpec]);

  const highchartsSpec = React.useMemo(() => {
    const sanitized = sanitizeHighchartsPlotSpec(plotSpec);
    if (!sanitized.ok) return null;
    return sanitized as SanitizedHighchartsSpec;
  }, [plotSpec]);

  const highchartsOptions = React.useMemo(() => {
    if (!highchartsSpec) return null;
    return toHighchartsPreviewOptions(highchartsSpec.options);
  }, [highchartsSpec]);

  const [highchartsReady, setHighchartsReady] = React.useState(false);
  const [highchartsConstructor, setHighchartsConstructor] = React.useState<HighchartsConstructorType>('chart');

  React.useEffect(() => {
    if (!highchartsSpec) {
      setHighchartsReady(false);
      setHighchartsConstructor('chart');
      return;
    }

    let cancelled = false;
    setHighchartsReady(false);
    const moduleName = highchartsSpec.module;
    const requestedModules = highchartsSpec.modules || [];

    void loadHighchartsModules(moduleName, requestedModules)
      .then(() => {
        if (cancelled) return;
        setHighchartsConstructor(resolveHighchartsConstructor(moduleName));
        setHighchartsReady(true);
      })
      .catch(() => {
        if (cancelled) return;
        setHighchartsReady(false);
      });

    return () => {
      cancelled = true;
    };
  }, [highchartsSpec]);

  if (!echartsOption && !highchartsOptions) return null;

  return (
    <div style={{ height }} className="w-full overflow-hidden">
      {echartsOption ? (
        <ReactECharts
          option={echartsOption}
          notMerge
          lazyUpdate
          style={{ width: '100%', height: '100%' }}
          opts={{ renderer: 'canvas', devicePixelRatio: 2 }}
        />
      ) : highchartsOptions ? (
        highchartsReady ? (
          <HighchartsReact
            highcharts={Highcharts}
            constructorType={highchartsConstructor}
            options={highchartsOptions}
            containerProps={{ style: { width: '100%', height: '100%' } }}
            immutable
            updateArgs={[true, true, true]}
          />
        ) : (
          <div className="h-full w-full bg-muted/20 animate-pulse" />
        )
      ) : null}
    </div>
  );
};

const InlineChartPreview = React.memo(InlineChartPreviewInner);
InlineChartPreview.displayName = 'InlineChartPreview';

export default InlineChartPreview;
